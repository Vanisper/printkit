package jobs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vanisper/printkit/internal/render"
)

type renderFunc func(context.Context, []byte) ([]byte, string, error)

func (f renderFunc) Render(ctx context.Context, document []byte, _ render.Settings) ([]byte, string, error) {
	return f(ctx, document)
}

func openTestService(t *testing.T, path string, opts Options) *Service {
	t.Helper()
	if path == "" {
		path = filepath.Join(t.TempDir(), "jobs.db")
	}
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	s, err := Open(context.Background(), path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func runService(t *testing.T, s *Service) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	var once sync.Once
	stop := func() {
		t.Helper()
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("执行器未及时退出")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func submitTest(t *testing.T, s *Service, req Request) Job {
	t.Helper()
	job, created, err := s.Submit(context.Background(), req)
	if err != nil || !created {
		t.Fatalf("提交失败: created=%v err=%v", created, err)
	}
	return job
}

func awaitState(t *testing.T, s *Service, caller, id string, state State) Job {
	t.Helper()
	deadline := time.After(5 * time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		job, err := s.Get(context.Background(), caller, id)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == state {
			return job
		}
		if job.State == StateFailed && state != StateFailed {
			t.Fatalf("任务意外失败: %s", job.Error)
		}
		select {
		case <-deadline:
			t.Fatalf("等待 %s 超时，当前 %s", state, job.State)
		case <-ticker.C:
		}
	}
}

func pdfRequest(key string) Request {
	return Request{CallerID: "demo", Key: key, Kind: KindPDF, Title: "测试单据", Document: []byte("%PDF-1.7\n原样字节\x00\xff")}
}

func TestConcurrentIdempotencyAndConflicts(t *testing.T) {
	s := openTestService(t, "", Options{})
	req := pdfRequest("same-key")
	const count = 24
	var wg sync.WaitGroup
	results := make(chan Job, count)
	var createdCount atomic.Int32
	for range count {
		wg.Go(func() {
			job, created, err := s.Submit(context.Background(), req)
			if err != nil {
				t.Error(err)
				return
			}
			if created {
				createdCount.Add(1)
			}
			results <- job
		})
	}
	wg.Wait()
	close(results)
	if createdCount.Load() != 1 {
		t.Fatalf("新建任务数=%d", createdCount.Load())
	}
	var id string
	for job := range results {
		if id == "" {
			id = job.ID
		}
		if job.ID != id {
			t.Fatalf("同一请求返回不同任务: %s / %s", id, job.ID)
		}
	}
	for _, change := range []func(*Request){
		func(r *Request) { r.Document = []byte("%PDF-1.7\nchanged") },
		func(r *Request) { r.Title = "不同标题" },
		func(r *Request) { r.Kind = KindHTML },
	} {
		changed := req
		change(&changed)
		if _, _, err := s.Submit(context.Background(), changed); !errors.Is(err, ErrConflict) {
			t.Fatalf("期望冲突，得到 %v", err)
		}
	}
	other := req
	other.CallerID = "another-caller"
	if job := submitTest(t, s, other); job.ID == id {
		t.Fatal("不同调用方共用了任务")
	}
}

func TestPDFPreservedAndCallerIsolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s := openTestService(t, path, Options{Renderer: renderFunc(func(context.Context, []byte) ([]byte, string, error) {
		t.Error("PDF 不应调用渲染器")
		return nil, "", errors.New("unexpected")
	})})
	req := pdfRequest("pdf")
	job := submitTest(t, s, req)
	if _, _, err := s.Artifact(context.Background(), req.CallerID, job.ID); !errors.Is(err, ErrNotReady) {
		t.Fatalf("未完成产物: %v", err)
	}
	stop := runService(t, s)
	finished := awaitState(t, s, req.CallerID, job.ID, StateOutputReady)
	artifact, digest, err := s.Artifact(context.Background(), req.CallerID, job.ID)
	if err != nil || !bytes.Equal(artifact, req.Document) {
		t.Fatalf("PDF 内容发生变化: %v", err)
	}
	if digest != sha256Hex(req.Document) || finished.InputSHA256 != digest || finished.ArtifactSHA256 != digest {
		t.Fatal("摘要不一致")
	}
	if finished.Mode != "artifact" || finished.Engine != "passthrough" || finished.OutputSize != int64(len(req.Document)) {
		t.Fatalf("产物元数据错误: %+v", finished)
	}
	if len(finished.Attempts) != 1 || finished.Attempts[0].FinishedAt == nil {
		t.Fatalf("缺少执行记录: %+v", finished.Attempts)
	}
	for _, caller := range []string{"another-caller", ""} {
		if _, err := s.Get(context.Background(), caller, job.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("任务归属检查: %v", err)
		}
		if _, _, err := s.Artifact(context.Background(), caller, job.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("产物归属检查: %v", err)
		}
	}
	stop()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestService(t, path, Options{})
	persisted, err := reopened.Get(context.Background(), req.CallerID, job.ID)
	if err != nil || persisted.State != StateOutputReady {
		t.Fatalf("重启后任务丢失: %+v %v", persisted, err)
	}
	artifact, _, err = reopened.Artifact(context.Background(), req.CallerID, job.ID)
	if err != nil || !bytes.Equal(artifact, req.Document) {
		t.Fatalf("重启后产物丢失: %v", err)
	}
}

func TestValidationRejectsInvalidOrLargeInputs(t *testing.T) {
	s := openTestService(t, "", Options{MaxInputBytes: 100})
	for _, test := range []struct {
		name   string
		change func(*Request)
		want   error
	}{
		{"empty caller", func(r *Request) { r.CallerID = "" }, ErrInvalid},
		{"long caller", func(r *Request) { r.CallerID = strings.Repeat("x", 129) }, ErrInvalid},
		{"empty key", func(r *Request) { r.Key = "" }, ErrInvalid},
		{"long key", func(r *Request) { r.Key = strings.Repeat("x", 129) }, ErrInvalid},
		{"control key", func(r *Request) { r.Key = "x\ny" }, ErrInvalid},
		{"padded key", func(r *Request) { r.Key = " key" }, ErrInvalid},
		{"long title", func(r *Request) { r.Title = strings.Repeat("x", 257) }, ErrInvalid},
		{"control title", func(r *Request) { r.Title = "x\ny" }, ErrInvalid},
		{"invalid unicode", func(r *Request) { r.Title = "\xff" }, ErrInvalid},
		{"unknown kind", func(r *Request) { r.Kind = "text" }, ErrInvalid},
		{"empty document", func(r *Request) { r.Document = nil }, ErrInvalid},
		{"invalid pdf", func(r *Request) { r.Document = []byte("not a PDF") }, ErrInvalid},
		{"too large", func(r *Request) { r.Document = append([]byte("%PDF-"), bytes.Repeat([]byte("x"), 96)...) }, ErrTooLarge},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := pdfRequest(test.name)
			test.change(&req)
			if _, _, err := s.Submit(context.Background(), req); !errors.Is(err, test.want) {
				t.Fatalf("期望 %v，得到 %v", test.want, err)
			}
		})
	}
	for _, id := range []string{"", strings.Repeat("a", 1000), "../../file", strings.Repeat("a", 32)} {
		if _, err := s.Get(context.Background(), "demo", id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("无效或不存在的 id: %v", err)
		}
	}
}

func TestExecutionFailuresAreTerminal(t *testing.T) {
	for _, test := range []struct {
		name      string
		renderer  renderFunc
		maxOutput int64
		want      string
	}{
		{"render failure", func(context.Context, []byte) ([]byte, string, error) {
			return nil, "fake", errors.New("图片资源加载失败")
		}, 0, "图片资源加载失败"},
		{"large output", func(context.Context, []byte) ([]byte, string, error) { return []byte("%PDF-123456789"), "fake", nil }, 8, "产物为"},
		{"invalid output", func(context.Context, []byte) ([]byte, string, error) { return []byte("not a pdf"), "fake", nil }, 0, "未返回 PDF"},
		{"timeout", func(ctx context.Context, _ []byte) ([]byte, string, error) {
			<-ctx.Done()
			return nil, "fake", ctx.Err()
		}, 0, "执行超时"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := openTestService(t, "", Options{Renderer: test.renderer, MaxOutputBytes: test.maxOutput, ExecutionTimeout: 40 * time.Millisecond})
			req := Request{CallerID: "demo", Key: "fail", Kind: KindHTML, Document: []byte("<html>sample</html>")}
			job := submitTest(t, s, req)
			runService(t, s)
			failed := awaitState(t, s, req.CallerID, job.ID, StateFailed)
			if !strings.Contains(failed.Error, test.want) {
				t.Fatalf("错误缺少原因: %s", failed.Error)
			}
			if len(failed.Attempts) != 1 || failed.Attempts[0].FinishedAt == nil || failed.Attempts[0].Error != failed.Error {
				t.Fatalf("失败记录错误: %+v", failed)
			}
			if _, _, err := s.Artifact(context.Background(), req.CallerID, job.ID); !errors.Is(err, ErrNotReady) {
				t.Fatalf("失败任务产物: %v", err)
			}
			var artifact []byte
			if err := s.db.QueryRow(`SELECT artifact FROM jobs WHERE id = ?`, job.ID).Scan(&artifact); err != nil || artifact != nil {
				t.Fatalf("失败后残留产物: %v", err)
			}
		})
	}
}

func TestInterruptedTaskRecoversOnceAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	started := make(chan struct{}, 2)
	blocking := renderFunc(func(ctx context.Context, _ []byte) ([]byte, string, error) {
		started <- struct{}{}
		<-ctx.Done()
		return nil, "fake", ctx.Err()
	})
	first := openTestService(t, path, Options{Renderer: blocking})
	req := Request{CallerID: "demo", Key: "recover", Kind: KindHTML, Document: []byte("<html>recover</html>")}
	job := submitTest(t, first, req)
	stopFirst := runService(t, first)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("首次执行未开始")
	}
	stopFirst()
	interrupted, err := first.Get(context.Background(), req.CallerID, job.ID)
	if err != nil || interrupted.State != StateRendering || interrupted.Attempts[0].FinishedAt != nil {
		t.Fatalf("中断状态错误: %+v %v", interrupted, err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second := openTestService(t, path, Options{Renderer: blocking})
	stopSecond := runService(t, second)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("恢复执行未开始")
	}
	recovered, err := second.Get(context.Background(), req.CallerID, job.ID)
	if err != nil || len(recovered.Attempts) != 2 || recovered.Attempts[0].FinishedAt == nil || !strings.Contains(recovered.Attempts[0].Error, "中断") {
		t.Fatalf("恢复记录错误: %+v %v", recovered, err)
	}
	stopSecond()
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	third := openTestService(t, path, Options{Renderer: renderFunc(func(context.Context, []byte) ([]byte, string, error) {
		t.Error("超过恢复上限后不应再次执行")
		return []byte("%PDF-1.7"), "fake", nil
	})})
	runService(t, third)
	failed := awaitState(t, third, req.CallerID, job.ID, StateFailed)
	if !strings.Contains(failed.Error, "恢复上限") || len(failed.Attempts) != 2 || failed.Attempts[1].FinishedAt == nil {
		t.Fatalf("恢复上限未持久化: %+v", failed)
	}
}

func TestSubmissionContextIsIndependentAndQueueIsFIFO(t *testing.T) {
	entered := make(chan string, 3)
	release := make(chan struct{})
	s := openTestService(t, "", Options{Renderer: renderFunc(func(ctx context.Context, input []byte) ([]byte, string, error) {
		entered <- string(input)
		select {
		case <-release:
			return []byte("%PDF-1.7"), "fake", nil
		case <-ctx.Done():
			return nil, "fake", ctx.Err()
		}
	})})
	submitCtx, cancelSubmit := context.WithCancel(context.Background())
	req := Request{CallerID: "demo", Key: "first", Kind: KindHTML, Document: []byte("first")}
	job, _, err := s.Submit(submitCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	cancelSubmit()
	req.Key, req.Document = "second", []byte("second")
	second := submitTest(t, s, req)
	req.Key, req.Document = "third", []byte("third")
	third := submitTest(t, s, req)
	runService(t, s)
	select {
	case input := <-entered:
		if input != "first" {
			t.Fatalf("首任务顺序错误: %s", input)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("提交请求结束后任务未执行")
	}
	select {
	case input := <-entered:
		t.Fatalf("并发执行了任务 %s", input)
	default:
	}
	close(release)
	for _, expected := range []string{"second", "third"} {
		select {
		case input := <-entered:
			if input != expected {
				t.Fatalf("队列顺序错误: %s / %s", input, expected)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("后续任务未执行")
		}
	}
	for _, id := range []string{job.ID, second.ID, third.ID} {
		awaitState(t, s, "demo", id, StateOutputReady)
	}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("同一服务重复启动未被拒绝")
	}
}

func TestSchemaVersionAndQueuedTasksSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s := openTestService(t, path, Options{})
	job := submitTest(t, s, pdfRequest("persisted-queue"))
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("数据库版本错误: %d %v", version, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestService(t, path, Options{})
	stop := runService(t, reopened)
	awaitState(t, reopened, "demo", job.ID, StateOutputReady)
	stop()
	if _, err := reopened.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if next, err := Open(context.Background(), path, Options{}); err == nil {
		next.Close()
		t.Fatal("未来版本数据库未被拒绝")
	}
}
