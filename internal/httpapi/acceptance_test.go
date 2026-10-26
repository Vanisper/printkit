package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Vanisper/printkit/internal/jobs"
	"github.com/Vanisper/printkit/internal/render"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/chromedp"
)

func TestAcceptanceCrossOriginDemo(t *testing.T) {
	if os.Getenv("PRINTKIT_ACCEPTANCE_TEST") != "1" {
		t.Skip("设置 PRINTKIT_ACCEPTANCE_TEST=1 后验证跨域页面")
	}
	files, err := fs.Sub(demoFiles, "demo")
	if err != nil {
		t.Fatal(err)
	}
	page := httptest.NewServer(http.StripPrefix("/demo/", http.FileServerFS(files)))
	defer page.Close()
	renderer, err := render.New(render.Options{ChromePath: os.Getenv("PRINTKIT_CHROME_PATH")})
	if err != nil {
		t.Fatal(err)
	}
	service, err := jobs.Open(context.Background(), filepath.Join(t.TempDir(), "jobs.db"), jobs.Options{
		Renderer: renderer, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &integrationApp{service: service, server: httptest.NewUnstartedServer(nil)}
	t.Cleanup(func() { app.close(t) })
	handler, err := New(service, Options{
		AllowedHosts: []string{app.server.Listener.Addr().String()},
		Callers:      []Caller{{ID: "alice", Token: integrationAliceToken, Origins: []string{page.URL}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var preflights atomic.Int64
	app.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions && r.Header.Get("Origin") == page.URL {
			preflights.Add(1)
		}
		handler.ServeHTTP(w, r)
	})
	app.server.Start()
	app.startRunner()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.Flag("no-sandbox", false))
	if path := os.Getenv("PRINTKIT_CHROME_PATH"); path != "" {
		opts = append(opts, chromedp.ExecPath(path))
	}
	allocator, cancelAllocator := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAllocator()
	tab, cancelTab := chromedp.NewContext(allocator)
	defer cancelTab()
	downloadDir := t.TempDir()
	if _, err := chromedp.CallBrowser(tab, browser.SetDownloadBehavior, browser.SetDownloadBehaviorParams{
		Behavior: browser.SetDownloadBehaviorBehaviorAllow, DownloadPath: downloadDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Do(tab,
		chromedp.Navigate(page.URL+"/demo/"),
		chromedp.SetValue(chromedp.CSS("#api-url"), app.server.URL),
		chromedp.SetValue(chromedp.CSS("#token"), integrationAliceToken),
		chromedp.Click(chromedp.CSS("#submit-button")),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Run(tab, chromedp.Poll[bool](`document.querySelector(".download") !== null`)); err != nil {
		feedback, _ := chromedp.Run(tab, chromedp.Evaluate[string](`document.getElementById("feedback").textContent`))
		t.Fatalf("页面提交未生成产物：%v；%s", err, feedback)
	}
	id, err := chromedp.Run(tab, chromedp.Evaluate[string](`document.querySelector(".job-id").textContent`))
	if err != nil {
		t.Fatal(err)
	}
	// 重新加载页面会清空内存中的令牌与任务，通过编号恢复查询后再下载
	if err := chromedp.Do(tab,
		chromedp.Navigate(page.URL+"/demo/"),
		chromedp.SetValue(chromedp.CSS("#api-url"), app.server.URL),
		chromedp.SetValue(chromedp.CSS("#token"), integrationAliceToken),
		chromedp.SetValue(chromedp.CSS("#lookup-id"), id),
		chromedp.Click(chromedp.CSS("#lookup-form button")),
		chromedp.Click(chromedp.CSS(".download")),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := chromedp.Run(tab, chromedp.Poll[bool](`document.getElementById("feedback").textContent.includes("SHA-256 校验一致")`)); err != nil {
		t.Fatalf("页面下载摘要校验未通过：%v", err)
	}
	var downloaded []byte
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		downloaded, err = os.ReadFile(filepath.Join(downloadDir, "document.pdf"))
		if err == nil {
			break
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("浏览器未完成文件下载")
		case <-ticker.C:
		}
	}
	data, _ := integrationGet(t, app, "/v1/jobs/"+id, integrationAliceToken, http.StatusOK)
	var job jobs.Job
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	if digest := fmt.Sprintf("%x", sha256.Sum256(downloaded)); digest != job.ArtifactSHA256 {
		t.Fatalf("浏览器落盘文件与任务摘要不同：%s != %s", digest, job.ArtifactSHA256)
	}
	if preflights.Load() == 0 {
		t.Fatal("没有观察到跨域预检请求")
	}
	t.Logf("跨域页面提交、重新查询和下载校验通过：%s，%d 次预检", id, preflights.Load())
}
