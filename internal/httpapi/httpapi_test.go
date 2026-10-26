package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vanisper/printkit/internal/jobs"
)

type backendStub struct {
	requests []jobs.Request
	created  bool
	err      error
	owner    string
	artifact []byte
}

func (b *backendStub) Submit(_ context.Context, request jobs.Request) (jobs.Job, bool, error) {
	b.requests = append(b.requests, request)
	return jobs.Job{ID: "job-1", Kind: request.Kind, Title: request.Title, State: jobs.StateQueued, Mode: "artifact"}, b.created, b.err
}

func (b *backendStub) Get(_ context.Context, callerID, id string) (jobs.Job, error) {
	if callerID != b.owner || id != "job-1" {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return jobs.Job{ID: id, Mode: "artifact", State: jobs.StateOutputReady}, b.err
}

func (b *backendStub) Artifact(_ context.Context, callerID, id string) ([]byte, string, error) {
	if callerID != b.owner || id != "job-1" {
		return nil, "", jobs.ErrNotFound
	}
	digest := sha256.Sum256(b.artifact)
	return b.artifact, hex.EncodeToString(digest[:]), b.err
}

func testOptions() Options {
	return Options{
		AllowedHosts:  []string{"127.0.0.1:17899", "localhost:17899", "[::1]:17899"},
		MaxInputBytes: 512,
		Callers: []Caller{
			{ID: "alice", Token: "alice-token", Origins: []string{"http://localhost:5173"}},
			{ID: "bob", Token: "bob-token", Origins: []string{"http://127.0.0.1:5174"}},
		},
	}
}

func testHandler(t *testing.T, backend *backendStub) http.Handler {
	t.Helper()
	handler, err := New(backend, testOptions())
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func call(handler http.Handler, method, path, token, origin string, body io.Reader, contentType string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://127.0.0.1:17899"+path, body)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Idempotency-Key", "request-1")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func requireStatus(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("状态码 = %d，期望 %d；正文：%s", response.Code, status, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("响应未禁止缓存")
	}
}

type inputPart struct {
	name string
	data string
}

func multipartBody(t *testing.T, parts ...inputPart) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, input := range parts {
		var part io.Writer
		var err error
		if input.name == "document" {
			part, err = writer.CreateFormFile(input.name, "document.pdf")
		} else {
			part, err = writer.CreateFormField(input.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, input.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), writer.FormDataContentType()
}

func TestAuthorizationAndOrigin(t *testing.T) {
	backend := &backendStub{owner: "alice"}
	handler := testHandler(t, backend)
	for _, test := range []struct {
		name, token, origin string
		status              int
	}{
		{"无令牌", "", "", http.StatusUnauthorized},
		{"错误令牌", "invalid", "", http.StatusUnauthorized},
		{"CLI 令牌", "alice-token", "", http.StatusOK},
		{"来源已授权", "alice-token", "http://localhost:5173", http.StatusOK},
		{"来源不在名单", "alice-token", "https://untrusted.example", http.StatusForbidden},
		{"拒绝其他调用方来源", "alice-token", "http://127.0.0.1:5174", http.StatusForbidden},
		{"拒绝 null 来源", "alice-token", "null", http.StatusForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := call(handler, http.MethodGet, "/v1/jobs/job-1", test.token, test.origin, nil, "")
			requireStatus(t, response, test.status)
			allowed := response.Header().Get("Access-Control-Allow-Origin")
			if test.status == http.StatusOK && allowed != test.origin {
				t.Fatalf("CORS 来源 = %q，期望 %q", allowed, test.origin)
			}
			if test.status != http.StatusOK && allowed != "" {
				t.Fatalf("拒绝请求仍返回了 CORS 来源 %q", allowed)
			}
		})
	}
	for _, header := range []string{"Authorization", "Origin"} {
		t.Run("拒绝重复"+header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17899/v1/jobs/job-1", nil)
			req.Header.Set("Authorization", "Bearer alice-token")
			req.Header.Set("Origin", "http://localhost:5173")
			req.Header.Add(header, req.Header.Get(header))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			status := http.StatusForbidden
			if header == "Authorization" {
				status = http.StatusUnauthorized
			}
			requireStatus(t, response, status)
		})
	}
}

func TestHostGuardAndPublicResources(t *testing.T) {
	handler := testHandler(t, &backendStub{})
	for _, host := range []string{"attacker.example:17899", "localhost.evil:17899", "127.0.0.1:9999", ""} {
		t.Run(host, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:17899/healthz", nil)
			req.Host = host
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			requireStatus(t, response, http.StatusForbidden)
		})
	}
	for _, path := range []string{"/healthz", "/demo/", "/demo/app.js", "/demo/style.css"} {
		response := call(handler, http.MethodGet, path, "", "", nil, "")
		requireStatus(t, response, http.StatusOK)
		if strings.Contains(response.Body.String(), "alice-token") || strings.Contains(response.Body.String(), "bob-token") {
			t.Fatal("公开资源泄露配置令牌")
		}
	}
}

func TestPreflight(t *testing.T) {
	handler := testHandler(t, &backendStub{})
	for _, test := range []struct {
		name, path, origin, method, headers string
		status                              int
	}{
		{"允许提交", "/v1/jobs", "http://localhost:5173", "POST", "authorization, content-type, idempotency-key", 204},
		{"允许另一来源查询", "/v1/jobs/job-1", "http://127.0.0.1:5174", "GET", "Authorization", 204},
		{"拒绝来源", "/v1/jobs", "https://evil.example", "POST", "authorization", 403},
		{"拒绝空来源", "/v1/jobs", "", "POST", "authorization", 403},
		{"拒绝方法", "/v1/jobs/job-1", "http://localhost:5173", "POST", "authorization", 403},
		{"拒绝请求头", "/v1/jobs", "http://localhost:5173", "POST", "authorization, x-secret", 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:17899"+test.path, nil)
			req.Header.Set("Origin", test.origin)
			req.Header.Set("Access-Control-Request-Method", test.method)
			req.Header.Set("Access-Control-Request-Headers", test.headers)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, req)
			requireStatus(t, response, test.status)
			if test.status == 204 && response.Header().Get("Access-Control-Allow-Origin") != test.origin {
				t.Fatal("预检未返回准确来源")
			}
		})
	}
}

func TestSubmitParsingAndIdempotency(t *testing.T) {
	backend := &backendStub{created: true}
	handler := testHandler(t, backend)
	body, contentType := multipartBody(t, inputPart{"metadata", `{"kind":"pdf","title":"单据"}`}, inputPart{"document", "%PDF-test"})
	response := call(handler, "POST", "/v1/jobs", "alice-token", "http://localhost:5173", bytes.NewReader(body), contentType)
	requireStatus(t, response, http.StatusAccepted)
	if response.Header().Get("Location") != "/v1/jobs/job-1" || len(backend.requests) != 1 {
		t.Fatalf("受理结果不完整：%s", response.Body.String())
	}
	received := backend.requests[0]
	if received.CallerID != "alice" || received.Key != "request-1" || received.Title != "单据" || received.Kind != jobs.KindPDF || string(received.Document) != "%PDF-test" {
		t.Fatalf("请求解析错误：%+v", received)
	}
	backend.created = false
	response = call(handler, "POST", "/v1/jobs", "alice-token", "", bytes.NewReader(body), contentType)
	requireStatus(t, response, http.StatusOK)
	backend.err = jobs.ErrConflict
	response = call(handler, "POST", "/v1/jobs", "alice-token", "", bytes.NewReader(body), contentType)
	requireStatus(t, response, http.StatusConflict)
}

func TestRejectMalformedInputs(t *testing.T) {
	metadata := inputPart{"metadata", `{"kind":"html","title":"样例"}`}
	document := inputPart{"document", "<!doctype html><p>样例</p>"}
	for _, test := range []struct {
		name   string
		parts  []inputPart
		status int
	}{
		{"缺少 metadata", []inputPart{document}, 400},
		{"缺少 document", []inputPart{metadata}, 400},
		{"文档为空", []inputPart{metadata, {"document", ""}}, 400},
		{"重复文档", []inputPart{metadata, document, document}, 400},
		{"重复元数据", []inputPart{metadata, metadata, document}, 400},
		{"未知字段", []inputPart{metadata, document, {"extra", "a"}}, 400},
		{"伪造调用方", []inputPart{{"metadata", `{"kind":"html","caller_id":"bob"}`}, document}, 400},
		{"JSON 后有额外对象", []inputPart{{"metadata", `{"kind":"html"}{}`}, document}, 400},
		{"JSON 非对象", []inputPart{{"metadata", `[]`}, document}, 400},
		{"未知格式", []inputPart{{"metadata", `{"kind":"url"}`}, document}, 400},
		{"文档过大", []inputPart{metadata, {"document", strings.Repeat("a", 513)}}, 413},
		{"元数据过大", []inputPart{{"metadata", strings.Repeat("a", metadataLimit+1)}, document}, 413},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &backendStub{}
			body, contentType := multipartBody(t, test.parts...)
			response := call(testHandler(t, backend), "POST", "/v1/jobs", "alice-token", "", bytes.NewReader(body), contentType)
			requireStatus(t, response, test.status)
			if len(backend.requests) != 0 {
				t.Fatal("非法输入调用了任务服务")
			}
		})
	}
	for _, key := range []string{"", " padded ", strings.Repeat("a", 129)} {
		t.Run("非法请求标识"+key, func(t *testing.T) {
			body, contentType := multipartBody(t, metadata, document)
			req := httptest.NewRequest("POST", "http://127.0.0.1:17899/v1/jobs", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer alice-token")
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Idempotency-Key", key)
			response := httptest.NewRecorder()
			testHandler(t, &backendStub{}).ServeHTTP(response, req)
			requireStatus(t, response, 400)
		})
	}
	t.Run("限制 multipart 尾部总量", func(t *testing.T) {
		body, contentType := multipartBody(t, metadata, document)
		body = append(body, bytes.Repeat([]byte("x"), multipartExtra+513)...)
		response := call(testHandler(t, &backendStub{}), "POST", "/v1/jobs", "alice-token", "", bytes.NewReader(body), contentType)
		requireStatus(t, response, 413)
	})
	t.Run("缺少结束边界", func(t *testing.T) {
		body, contentType := multipartBody(t, metadata, document)
		response := call(testHandler(t, &backendStub{}), "POST", "/v1/jobs", "alice-token", "", bytes.NewReader(body[:len(body)-30]), contentType)
		requireStatus(t, response, 400)
	})
}

func TestOwnershipAndArtifact(t *testing.T) {
	backend := &backendStub{owner: "alice", artifact: []byte("%PDF-original")}
	handler := testHandler(t, backend)
	for _, path := range []string{"/v1/jobs/job-1", "/v1/jobs/job-1/artifact"} {
		response := call(handler, "GET", path, "bob-token", "", nil, "")
		requireStatus(t, response, http.StatusNotFound)
	}
	response := call(handler, "GET", "/v1/jobs/job-1/artifact", "alice-token", "http://localhost:5173", nil, "")
	requireStatus(t, response, http.StatusOK)
	if response.Body.String() != string(backend.artifact) || response.Header().Get("Content-Type") != "application/pdf" {
		t.Fatal("下载文档内容或格式变化")
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(backend.artifact))
	if response.Header().Get("X-Content-SHA256") != digest || response.Header().Get("ETag") != `"`+digest+`"` {
		t.Fatal("下载缺少正确摘要")
	}
	if response.Header().Get("Content-Disposition") != `attachment; filename="document.pdf"` {
		t.Fatal("下载文件名不安全")
	}
}

func TestErrorMappingDoesNotLeakDetails(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{jobs.ErrInvalid, 400, "invalid_request"},
		{jobs.ErrConflict, 409, "idempotency_conflict"},
		{jobs.ErrNotReady, 409, "artifact_not_ready"},
		{jobs.ErrTooLarge, 413, "input_too_large"},
		{jobs.ErrNotFound, 404, "not_found"},
		{errors.New("sqlite: sensitive internal path"), 500, "internal_error"},
	} {
		t.Run(test.code, func(t *testing.T) {
			var logs bytes.Buffer
			opts := testOptions()
			opts.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			handler, err := New(&backendStub{owner: "alice", err: fmt.Errorf("backend detail: %w", test.err)}, opts)
			if err != nil {
				t.Fatal(err)
			}
			response := call(handler, "GET", "/v1/jobs/job-1/artifact", "alice-token", "", nil, "")
			requireStatus(t, response, test.status)
			var body struct {
				Error struct{ Code, Message string }
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != test.code || strings.Contains(response.Body.String(), "sensitive") || strings.Contains(response.Body.String(), "backend detail") {
				t.Fatalf("错误内容不符合契约：%s", response.Body.String())
			}
			if test.status != http.StatusInternalServerError && logs.Len() != 0 {
				t.Fatalf("预期的业务错误不应写入异常日志：%s", logs.String())
			}
		})
	}
}

func TestInternalErrorsLogCorrelationWithoutRequestContents(t *testing.T) {
	const detail = "sqlite: database or disk is full"
	const document = "%PDF-private-document-contents"
	const title = "private-document-title"
	for _, test := range []struct {
		name, method, path, operation, identifierName, identifier string
	}{
		{"提交", "POST", "/v1/jobs", "POST /v1/jobs", "request_key", "request-1"},
		{"查询", "GET", "/v1/jobs/job-1", "GET /v1/jobs/{id}", "job_id", "job-1"},
		{"下载", "GET", "/v1/jobs/job-1/artifact", "GET /v1/jobs/{id}/artifact", "job_id", "job-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			opts := testOptions()
			opts.Logger = slog.New(slog.NewJSONHandler(&logs, nil))
			handler, err := New(&backendStub{owner: "alice", err: errors.New(detail)}, opts)
			if err != nil {
				t.Fatal(err)
			}
			body, contentType := multipartBody(t, inputPart{"metadata", `{"kind":"pdf","title":"` + title + `"}`}, inputPart{"document", document})
			response := call(handler, test.method, test.path+"?secret=private-query-value", "alice-token", "", bytes.NewReader(body), contentType)
			requireStatus(t, response, http.StatusInternalServerError)
			var record map[string]any
			decoder := json.NewDecoder(&logs)
			if err := decoder.Decode(&record); err != nil {
				t.Fatalf("未记录可读取的错误日志：%v", err)
			}
			if decoder.Decode(new(any)) != io.EOF {
				t.Fatal("一次异常应只记录一条日志")
			}
			for key, want := range map[string]string{
				"level": "ERROR", "operation": test.operation, "caller_id": "alice", "error": detail, test.identifierName: test.identifier,
			} {
				if record[key] != want {
					t.Errorf("日志字段 %s = %v，期望 %q", key, record[key], want)
				}
			}
			logged, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"alice-token", document, title, "private-query-value"} {
				if bytes.Contains(logged, []byte(secret)) {
					t.Errorf("日志包含请求中的敏感内容 %q", secret)
				}
			}
			if strings.Contains(response.Body.String(), detail) {
				t.Fatal("HTTP 响应泄露内部错误")
			}
		})
	}
}

func TestRejectAmbiguousConfiguration(t *testing.T) {
	for _, change := range []func(*Options){
		func(o *Options) { o.AllowedHosts = nil },
		func(o *Options) { o.AllowedHosts = []string{"*"} },
		func(o *Options) { o.Callers = nil },
		func(o *Options) { o.Callers[1].ID = o.Callers[0].ID },
		func(o *Options) { o.Callers[1].Token = o.Callers[0].Token },
		func(o *Options) { o.Callers[0].Token = "" },
		func(o *Options) { o.Callers[0].Origins = []string{"https://example.test/"} },
		func(o *Options) { o.Callers[0].Origins = []string{"https://*.example.test"} },
		func(o *Options) { o.MaxInputBytes = -1 },
	} {
		opts := testOptions()
		change(&opts)
		if _, err := New(&backendStub{}, opts); err == nil {
			t.Fatalf("错误配置未被拒绝：%+v", opts)
		}
	}
}

func TestCallerIDMatchesTaskContract(t *testing.T) {
	for _, test := range []struct {
		name string
		id   string
	}{
		{"空身份", ""},
		{"首部空白", " alice"},
		{"尾部空白", "alice\u3000"},
		{"超过字节上限", strings.Repeat("a", 129)},
		{"多字节超限", strings.Repeat("中", 43)},
		{"无效 UTF-8", "alice\xff"},
		{"ASCII 控制字符", "ali\x00ce"},
		{"Unicode 控制字符", "ali\u0085ce"},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts := testOptions()
			opts.Callers[0].ID = test.id
			if _, err := New(&backendStub{}, opts); err == nil {
				t.Fatalf("非法调用方身份未被拒绝：%q", test.id)
			}
		})
	}
	for _, id := range []string{"a", strings.Repeat("a", 128), strings.Repeat("中", 42) + "ab", "业务 应用"} {
		opts := testOptions()
		opts.Callers[0].ID = id
		if _, err := New(&backendStub{}, opts); err != nil {
			t.Fatalf("有效调用方身份 %q 被拒绝：%v", id, err)
		}
	}
}
