package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vanisper/printkit/internal/jobs"
	"github.com/Vanisper/printkit/internal/render"
)

const integrationAliceToken = "alice-integration-token-0123456789"
const integrationBobToken = "bob-integration-token-012345678901"

type integrationApp struct {
	service *jobs.Service
	server  *httptest.Server
	cancel  context.CancelFunc
	done    chan error
	closed  sync.Once
}

func openIntegrationApp(t *testing.T, path string, renderer jobs.Renderer) *integrationApp {
	t.Helper()
	service, err := jobs.Open(context.Background(), path, jobs.Options{
		Renderer: renderer,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &integrationApp{service: service, server: httptest.NewUnstartedServer(nil)}
	t.Cleanup(func() { app.close(t) })
	handler, err := New(service, Options{
		AllowedHosts: []string{app.server.Listener.Addr().String()},
		Callers: []Caller{
			{ID: "alice", Token: integrationAliceToken},
			{ID: "bob", Token: integrationBobToken},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	app.server.Config.Handler = handler
	app.server.Start()
	return app
}

func (app *integrationApp) startRunner() {
	ctx, cancel := context.WithCancel(context.Background())
	app.cancel = cancel
	app.done = make(chan error, 1)
	go func() { app.done <- app.service.Run(ctx) }()
}

func (app *integrationApp) close(t *testing.T) {
	t.Helper()
	app.closed.Do(func() {
		app.server.Close()
		if app.cancel != nil {
			app.cancel()
			select {
			case err := <-app.done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(10 * time.Second):
				t.Error("后台执行器未及时退出")
			}
		}
		if err := app.service.Close(); err != nil {
			t.Error(err)
		}
	})
}

func integrationCall(t *testing.T, app *integrationApp, req *http.Request, status int) ([]byte, http.Header) {
	t.Helper()
	response, err := app.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s 返回 %d，期望 %d：%s", req.Method, req.URL.Path, response.StatusCode, status, body)
	}
	return body, response.Header
}

func integrationSubmit(t *testing.T, app *integrationApp, ctx context.Context, key string, kind jobs.Kind, document []byte, status int) jobs.Job {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadata, err := writer.CreateFormField("metadata")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.NewEncoder(metadata).Encode(map[string]any{"kind": kind, "title": "端到端单据"}); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("document", "document."+string(kind))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(document); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, app.server.URL+"/v1/jobs", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+integrationAliceToken)
	req.Header.Set("Idempotency-Key", key)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	data, headers := integrationCall(t, app, req, status)
	var job jobs.Job
	if status == http.StatusAccepted || status == http.StatusOK {
		if err = json.Unmarshal(data, &job); err != nil {
			t.Fatal(err)
		}
		if headers.Get("Location") != "/v1/jobs/"+job.ID {
			t.Fatalf("受理响应缺少任务地址：%v", headers)
		}
	}
	return job
}

func integrationGet(t *testing.T, app *integrationApp, path, token string, status int) ([]byte, http.Header) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, app.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return integrationCall(t, app, req, status)
}

func awaitIntegrationOutput(t *testing.T, app *integrationApp, id string) jobs.Job {
	t.Helper()
	deadline := time.After(30 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, _ := integrationGet(t, app, "/v1/jobs/"+id, integrationAliceToken, http.StatusOK)
		var job jobs.Job
		if err := json.Unmarshal(data, &job); err != nil {
			t.Fatal(err)
		}
		if job.State == jobs.StateOutputReady {
			return job
		}
		if job.State == jobs.StateFailed {
			t.Fatalf("任务执行失败：%s", job.Error)
		}
		select {
		case <-deadline:
			t.Fatalf("等待产物超时，当前状态 %s", job.State)
		case <-ticker.C:
		}
	}
}

func TestHTTPPDFLifecycleWithSQLite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	app := openIntegrationApp(t, path, nil)
	// 该样本只验证传输保真，PDF 可解析性与可打印性不在接收路径的契约内
	document := []byte("%PDF-1.7\n%binary\x00\xfe\xff\n%%EOF\n")
	ctx, cancel := context.WithCancel(context.Background())
	job := integrationSubmit(t, app, ctx, "durable-pdf", jobs.KindPDF, document, http.StatusAccepted)
	cancel()
	if job.ID == "" || job.State != jobs.StateQueued || job.Mode != "artifact" {
		t.Fatalf("受理结果错误：%+v", job)
	}
	// HTTP 响应已读取关闭，提交上下文也已取消，此时才启动后台执行
	app.startRunner()
	ready := awaitIntegrationOutput(t, app, job.ID)
	digest := fmt.Sprintf("%x", sha256.Sum256(document))
	if ready.InputSHA256 != digest || ready.ArtifactSHA256 != digest || ready.OutputSize != int64(len(document)) || len(ready.Attempts) != 1 {
		t.Fatalf("产物元数据错误：%+v", ready)
	}
	data, headers := integrationGet(t, app, "/v1/jobs/"+job.ID+"/artifact", integrationAliceToken, http.StatusOK)
	if !bytes.Equal(data, document) || headers.Get("X-Content-SHA256") != digest || headers.Get("ETag") != `"`+digest+`"` || headers.Get("Content-Type") != "application/pdf" {
		t.Fatalf("下载产物或摘要不一致：%v", headers)
	}
	repeated := integrationSubmit(t, app, context.Background(), "durable-pdf", jobs.KindPDF, document, http.StatusOK)
	if repeated.ID != job.ID || repeated.State != jobs.StateOutputReady {
		t.Fatalf("重复请求未返回已有产物：%+v", repeated)
	}
	integrationSubmit(t, app, context.Background(), "durable-pdf", jobs.KindPDF, []byte("%PDF-1.7\nchanged"), http.StatusConflict)
	for _, suffix := range []string{"", "/artifact"} {
		integrationGet(t, app, "/v1/jobs/"+job.ID+suffix, integrationBobToken, http.StatusNotFound)
	}
	app.close(t)
	reopened := openIntegrationApp(t, path, nil)
	reopened.startRunner()
	persisted := awaitIntegrationOutput(t, reopened, job.ID)
	if persisted.ArtifactSHA256 != digest || len(persisted.Attempts) != 1 {
		t.Fatalf("已完成任务在重启后丢失或被重复执行：%+v", persisted)
	}
	data, _ = integrationGet(t, reopened, "/v1/jobs/"+job.ID+"/artifact", integrationAliceToken, http.StatusOK)
	if !bytes.Equal(data, document) {
		t.Fatal("重启后产物字节发生变化")
	}
}

func TestHTTPHTMLLifecycleWithChrome(t *testing.T) {
	if os.Getenv("PRINTKIT_CHROME_TEST") != "1" {
		t.Skip("设置 PRINTKIT_CHROME_TEST=1 后验证实际 Chrome 整链路")
	}
	renderer, err := render.New(render.Options{ChromePath: os.Getenv("PRINTKIT_CHROME_PATH")})
	if err != nil {
		t.Fatal(err)
	}
	app := openIntegrationApp(t, filepath.Join(t.TempDir(), "jobs.db"), renderer)
	app.startRunner()
	input := []byte(`<!doctype html><html lang="zh-CN"><meta charset="UTF-8"><style>@page { size: 80mm 120mm; margin: 10mm } h1 { color: navy }</style><h1>软件链路验收</h1><p>HTTP → SQLite → Chrome → PDF</p></html>`)
	job := integrationSubmit(t, app, context.Background(), "chrome-html", jobs.KindHTML, input, http.StatusAccepted)
	ready := awaitIntegrationOutput(t, app, job.ID)
	data, headers := integrationGet(t, app, "/v1/jobs/"+job.ID+"/artifact", integrationAliceToken, http.StatusOK)
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if !bytes.HasPrefix(data, []byte("%PDF-")) || headers.Get("X-Content-SHA256") != digest || ready.ArtifactSHA256 != digest || !strings.Contains(ready.Engine, "Chrome/") {
		t.Fatalf("Chrome 产物或执行元数据错误：%+v", ready)
	}
}
