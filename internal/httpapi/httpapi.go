// Package httpapi 提供文档产物任务的 HTTP 接口及验证页面
package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Vanisper/printkit/internal/jobs"
)

// Backend 通过调用方身份隔离任务及其产物
type Backend interface {
	// Submit 持久化受理任务，布尔返回值表示是否新建；同键不同内容返回 jobs.ErrConflict
	Submit(ctx context.Context, request jobs.Request) (jobs.Job, bool, error)
	// Get 返回任务状态；不存在或不属于调用方的任务均返回 jobs.ErrNotFound
	Get(ctx context.Context, callerID, jobID string) (jobs.Job, error)
	// Artifact 返回 PDF 及其 SHA-256；不存在或不属于调用方时返回 jobs.ErrNotFound，未就绪时返回 jobs.ErrNotReady
	Artifact(ctx context.Context, callerID, jobID string) ([]byte, string, error)
}

// Caller 定义获得本机服务访问授权的调用方
type Caller struct {
	// ID 在配置中唯一，用于隔离任务归属
	ID string
	// Token 为调用方独享的 Bearer 令牌
	Token string
	// Origins 是不含路径或通配符的准确 HTTP(S) 来源；为空时仅接受未携带 Origin 的请求
	Origins []string
}

// Options 配置 HTTP 入口的身份及请求边界
type Options struct {
	Callers []Caller
	// AllowedHosts 是允许的完整 Host，包含监听端口；不接受通配符
	AllowedHosts []string
	// MaxInputBytes 为单个文档上限，零值使用 jobs.DefaultMaxInputBytes
	MaxInputBytes int64
	// Logger 记录未预期的任务服务错误，nil 时使用 slog.Default()
	Logger *slog.Logger
}

type caller struct {
	id      string
	token   [sha256.Size]byte
	origins map[string]bool
}

type api struct {
	backend    Backend
	callers    []caller
	hosts      map[string]bool
	origins    map[string]bool
	inputLimit int64
	logger     *slog.Logger
}

//go:embed demo
var demoFiles embed.FS

const (
	metadataLimit  = 4 << 10
	multipartExtra = 64 << 10
)

// New 创建接口处理器；调用方、令牌及 Host 配置为空或有歧义时返回错误
func New(backend Backend, opts Options) (http.Handler, error) {
	if backend == nil || len(opts.Callers) == 0 || len(opts.AllowedHosts) == 0 {
		return nil, errors.New("HTTP 入口需要任务服务、调用方及允许的 Host")
	}
	if opts.MaxInputBytes == 0 {
		opts.MaxInputBytes = jobs.DefaultMaxInputBytes
	}
	if opts.MaxInputBytes < 1 || opts.MaxInputBytes > 1<<40 {
		return nil, errors.New("文档大小上限无效")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	a := &api{backend: backend, hosts: make(map[string]bool), origins: make(map[string]bool), inputLimit: opts.MaxInputBytes, logger: opts.Logger}
	for _, host := range opts.AllowedHosts {
		u, err := url.Parse("http://" + host)
		if err != nil || u.Host != host || u.Hostname() == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || strings.ContainsAny(host, "* \t\r\n") {
			return nil, fmt.Errorf("允许的 Host 无效：%q", host)
		}
		a.hosts[strings.ToLower(host)] = true
	}
	ids, tokens := make(map[string]bool), make(map[string]bool)
	for _, config := range opts.Callers {
		if config.ID == "" || len(config.ID) > 128 || !utf8.ValidString(config.ID) || strings.TrimSpace(config.ID) != config.ID || strings.IndexFunc(config.ID, unicode.IsControl) != -1 {
			return nil, errors.New("调用方身份须为 1 至 128 字节的 UTF-8 文本，且不含控制字符或首尾空白")
		}
		if config.Token == "" || strings.ContainsAny(config.Token, " \t\r\n") || ids[config.ID] || tokens[config.Token] {
			return nil, errors.New("调用方身份和令牌必须非空且互不重复")
		}
		ids[config.ID], tokens[config.Token] = true, true
		c := caller{id: config.ID, token: sha256.Sum256([]byte(config.Token)), origins: make(map[string]bool)}
		for _, origin := range config.Origins {
			u, err := url.Parse(origin)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(origin, "* \t\r\n") {
				return nil, fmt.Errorf("允许的 Origin 无效：%q", origin)
			}
			c.origins[origin], a.origins[origin] = true, true
		}
		a.callers = append(a.callers, c)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/jobs", a.authorize(a.submit))
	mux.HandleFunc("GET /v1/jobs/{id}", a.authorize(a.get))
	mux.HandleFunc("GET /v1/jobs/{id}/artifact", a.authorize(a.artifact))
	mux.HandleFunc("OPTIONS /v1/jobs", a.preflight)
	mux.HandleFunc("OPTIONS /v1/jobs/{id}", a.preflight)
	mux.HandleFunc("OPTIONS /v1/jobs/{id}/artifact", a.preflight)
	demo, err := fs.Sub(demoFiles, "demo")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /demo/", http.StripPrefix("/demo/", http.FileServerFS(demo)))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "接口不存在")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if !a.hosts[strings.ToLower(r.Host)] {
			writeError(w, http.StatusForbidden, "host_not_allowed", "请求 Host 不在允许列表中")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/demo/") {
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self' http://127.0.0.1:* http://localhost:*; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func (a *api) authorize(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		values := r.Header.Values("Authorization")
		var matched *caller
		if len(values) == 1 {
			parts := strings.Fields(values[0])
			if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
				token := sha256.Sum256([]byte(parts[1]))
				for i := range a.callers {
					if subtle.ConstantTimeCompare(token[:], a.callers[i].token[:]) == 1 {
						matched = &a.callers[i]
					}
				}
			}
		}
		if matched == nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized", "需要有效的调用方令牌")
			return
		}
		origins := r.Header.Values("Origin")
		if len(origins) > 0 {
			if len(origins) != 1 || !matched.origins[origins[0]] {
				writeError(w, http.StatusForbidden, "origin_not_allowed", "当前来源未获得该调用方授权")
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origins[0])
			w.Header().Set("Access-Control-Expose-Headers", "X-Content-SHA256, ETag")
		}
		next(w, r, matched.id)
	}
}

func (a *api) preflight(w http.ResponseWriter, r *http.Request) {
	for _, header := range []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"} {
		w.Header().Add("Vary", header)
	}
	origins, methods := r.Header.Values("Origin"), r.Header.Values("Access-Control-Request-Method")
	if len(origins) != 1 || !a.origins[origins[0]] {
		writeError(w, http.StatusForbidden, "origin_not_allowed", "当前来源不在允许列表中")
		return
	}
	expectedMethod := http.MethodGet
	if r.URL.Path == "/v1/jobs" {
		expectedMethod = http.MethodPost
	}
	if len(methods) != 1 || methods[0] != expectedMethod {
		writeError(w, http.StatusForbidden, "method_not_allowed", "该接口不允许请求的方法")
		return
	}
	allowedHeaders := map[string]bool{"authorization": true, "content-type": true, "idempotency-key": true}
	for _, value := range r.Header.Values("Access-Control-Request-Headers") {
		for header := range strings.SplitSeq(value, ",") {
			if !allowedHeaders[strings.ToLower(strings.TrimSpace(header))] {
				writeError(w, http.StatusForbidden, "header_not_allowed", "预检包含未允许的请求头")
				return
			}
		}
	}
	w.Header().Set("Access-Control-Allow-Origin", origins[0])
	w.Header().Set("Access-Control-Allow-Methods", expectedMethod)
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) submit(w http.ResponseWriter, r *http.Request, callerID string) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || len(keys[0]) == 0 || len(keys[0]) > 128 || strings.TrimSpace(keys[0]) != keys[0] || strings.ContainsAny(keys[0], "\r\n") {
		writeError(w, http.StatusBadRequest, "invalid_key", "需要 1 至 128 字节的 Idempotency-Key")
		return
	}
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if len(r.Header.Values("Content-Type")) != 1 || err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" || len(params["boundary"]) > 70 {
		writeError(w, http.StatusBadRequest, "invalid_multipart", "需要 multipart/form-data 文档请求")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.inputLimit+multipartExtra)
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_multipart", "无法读取 multipart 请求")
		return
	}
	parts := make(map[string][]byte, 2)
	for {
		part, err := reader.NextRawPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeParseError(w, err)
			return
		}
		name := part.FormName()
		if _, exists := parts[name]; exists || (name != "metadata" && name != "document") || (name == "document" && part.FileName() == "") || part.Header.Get("Content-Transfer-Encoding") != "" {
			writeError(w, http.StatusBadRequest, "invalid_multipart", "请求只能包含一个 metadata 和一个 document")
			return
		}
		limit := a.inputLimit
		if name == "metadata" {
			limit = metadataLimit
		}
		data, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil {
			writeParseError(w, err)
			return
		}
		if int64(len(data)) > limit {
			writeError(w, http.StatusRequestEntityTooLarge, "input_too_large", "文档或元数据超过大小限制")
			return
		}
		parts[name] = data
	}
	// 读取结束边界之后的数据，使总正文上限同样覆盖 multipart 尾部
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		writeParseError(w, err)
		return
	}
	if len(parts) != 2 || len(parts["document"]) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_multipart", "需要非空 document 及 metadata")
		return
	}
	var metadata struct {
		Kind  jobs.Kind `json:"kind"`
		Title string    `json:"title"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(parts["metadata"])))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_metadata", "metadata 必须是仅含 kind 和 title 的 JSON 对象")
		return
	}
	if decoder.Decode(new(any)) != io.EOF || (metadata.Kind != jobs.KindHTML && metadata.Kind != jobs.KindPDF) || len(metadata.Title) > 256 {
		writeError(w, http.StatusBadRequest, "invalid_metadata", "kind 必须为 html 或 pdf，title 最多 256 字节")
		return
	}
	job, created, err := a.backend.Submit(r.Context(), jobs.Request{CallerID: callerID, Key: keys[0], Kind: metadata.Kind, Title: metadata.Title, Document: parts["document"]})
	if err != nil {
		a.writeBackendError(w, r, callerID, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	w.Header().Set("Location", "/v1/jobs/"+url.PathEscape(job.ID))
	writeJSON(w, status, job)
}

func (a *api) get(w http.ResponseWriter, r *http.Request, callerID string) {
	job, err := a.backend.Get(r.Context(), callerID, r.PathValue("id"))
	if err != nil {
		a.writeBackendError(w, r, callerID, err)
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (a *api) artifact(w http.ResponseWriter, r *http.Request, callerID string) {
	data, digest, err := a.backend.Artifact(r.Context(), callerID, r.PathValue("id"))
	if err != nil {
		a.writeBackendError(w, r, callerID, err)
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `attachment; filename="document.pdf"`)
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Header().Set("X-Content-SHA256", digest)
	w.Header().Set("ETag", `"`+digest+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func writeParseError(w http.ResponseWriter, err error) {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		writeError(w, http.StatusRequestEntityTooLarge, "input_too_large", "请求正文超过大小限制")
		return
	}
	writeError(w, http.StatusBadRequest, "invalid_multipart", "multipart 请求不完整或格式错误")
}

func (a *api) writeBackendError(w http.ResponseWriter, r *http.Request, callerID string, err error) {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "任务不存在")
	case errors.Is(err, jobs.ErrConflict):
		writeError(w, http.StatusConflict, "idempotency_conflict", "请求标识已用于不同内容")
	case errors.Is(err, jobs.ErrNotReady):
		writeError(w, http.StatusConflict, "artifact_not_ready", "PDF 产物尚未就绪")
	case errors.Is(err, jobs.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "input_too_large", "文档超过大小限制")
	case errors.Is(err, jobs.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "任务参数无效")
	default:
		attrs := []slog.Attr{slog.String("operation", r.Pattern), slog.String("caller_id", callerID), slog.Any("error", err)}
		name, identifier := "job_id", r.PathValue("id")
		if r.Method == http.MethodPost {
			name, identifier = "request_key", r.Header.Get("Idempotency-Key")
		}
		// 路径和请求头均由调用方提供，只记录有界且不含控制字符的关联标识
		if identifier != "" && len(identifier) <= 128 && utf8.ValidString(identifier) && strings.TrimSpace(identifier) == identifier && strings.IndexFunc(identifier, unicode.IsControl) == -1 {
			attrs = append(attrs, slog.String(name, identifier))
		}
		a.logger.LogAttrs(r.Context(), slog.LevelError, "HTTP 任务操作失败", attrs...)
		writeError(w, http.StatusInternalServerError, "internal_error", "服务暂时无法处理请求")
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
