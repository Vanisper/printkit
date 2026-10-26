package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vanisper/printkit/internal/render"
)

type settingsRenderer func(context.Context, []byte, render.Settings) ([]byte, string, error)

func (f settingsRenderer) Render(ctx context.Context, document []byte, settings render.Settings) ([]byte, string, error) {
	return f(ctx, document, settings)
}

func TestStoredSettingsPreserveRequestIdentity(t *testing.T) {
	const stored = `{"paper":"A4","prefer_css_page_size":true,"print_background":true,"display_header_footer":false,"margin_mm":{"top":10,"right":10,"bottom":10,"left":10},"scale":1}`
	path := filepath.Join(t.TempDir(), "jobs.db")
	s := openTestService(t, path, Options{})
	req := Request{CallerID: "demo", Key: "existing", Kind: KindHTML, Document: []byte("<p>单据</p>")}
	job := submitTest(t, s, req)
	var snapshot string
	if err := s.db.QueryRow(`SELECT settings FROM jobs WHERE id = ?`, job.ID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot != stored {
		t.Fatalf("已受理任务的排版编码不兼容：%s", snapshot)
	}
	// 固定已受理请求的摘要，覆盖设置编码和摘要算法的兼容性
	if _, err := s.db.Exec(`UPDATE jobs SET request_sha256 = ? WHERE id = ?`, "4842135c83db23534a89f5ea17ae618b01d5b528b71c94da56e4019028674983", job.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestService(t, path, Options{})
	again, created, err := reopened.Submit(context.Background(), req)
	if err != nil || created || again.ID != job.ID {
		t.Fatalf("重启后相同请求未返回原任务：%+v created=%v err=%v", again, created, err)
	}
}

func TestQueuedTaskRendersPersistedSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.db")
	s := openTestService(t, path, Options{})
	req := Request{CallerID: "demo", Key: "snapshot", Kind: KindHTML, Document: []byte("<p>保存的设置</p>")}
	job := submitTest(t, s, req)
	want := render.DefaultSettings()
	want.MarginMM = render.Margins{Top: 12, Right: 13, Bottom: 14, Left: 15}
	want.Scale, want.PrintBackground = 0.75, false
	snapshot, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`UPDATE jobs SET settings = ?, request_sha256 = ? WHERE id = ?`, string(snapshot), requestDigest(req, string(snapshot)), job.ID); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	received := make(chan render.Settings, 1)
	reopened := openTestService(t, path, Options{Renderer: settingsRenderer(func(_ context.Context, document []byte, settings render.Settings) ([]byte, string, error) {
		if string(document) != string(req.Document) {
			t.Error("恢复后文档发生变化")
		}
		received <- settings
		return []byte("%PDF-snapshot"), "fake", nil
	})})
	again, created, err := reopened.Submit(context.Background(), req)
	if err != nil || created || again.ID != job.ID {
		t.Fatalf("保存的设置不同于当前默认时，重复请求未返回原任务：created=%v err=%v", created, err)
	}
	changed := req
	changed.Document = []byte("<p>不同文档</p>")
	if _, _, err := reopened.Submit(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("恢复旧设置后未检测到不同内容：%v", err)
	}
	runService(t, reopened)
	awaitState(t, reopened, req.CallerID, job.ID, StateOutputReady)
	if got := <-received; got != want {
		t.Fatalf("执行未使用持久化设置：got=%+v want=%+v", got, want)
	}
}

func TestInvalidStoredSettingsFailOnlyTheirTask(t *testing.T) {
	s := openTestService(t, "", Options{Renderer: settingsRenderer(func(_ context.Context, _ []byte, settings render.Settings) ([]byte, string, error) {
		if settings != render.DefaultSettings() {
			t.Error("无效设置传入了渲染器")
		}
		return []byte("%PDF-valid"), "fake", nil
	})})
	var invalid []Job
	for _, snapshot := range []string{"broken", "{}", "null", `{"paper":"A4","scale":0}`, `{"paper":"A3","scale":1}`, `{"paper":"A4","scale":1,"unknown":true}`, `{} {}`} {
		job := submitTest(t, s, Request{CallerID: "demo", Key: snapshot, Kind: KindHTML, Document: []byte("<p>无效设置</p>")})
		if _, err := s.db.Exec(`UPDATE jobs SET settings = ? WHERE id = ?`, snapshot, job.ID); err != nil {
			t.Fatal(err)
		}
		invalid = append(invalid, job)
	}
	valid := submitTest(t, s, Request{CallerID: "demo", Key: "valid", Kind: KindHTML, Document: []byte("<p>后续任务</p>")})
	runService(t, s)
	for _, job := range invalid {
		failed := awaitState(t, s, "demo", job.ID, StateFailed)
		if !strings.Contains(failed.Error, "排版设置") || len(failed.Attempts) != 1 || failed.Attempts[0].Error != failed.Error {
			t.Fatalf("设置错误未保留在任务记录中：%+v", failed)
		}
	}
	awaitState(t, s, "demo", valid.ID, StateOutputReady)
}
