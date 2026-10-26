package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const processTestChromeMode = "PRINTKIT_PROCESS_TEST_CHROME"
const processTestChromeMarker = "PRINTKIT_PROCESS_TEST_CHROME_MARKER"

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == WorkerCommand {
		if err := RunWorker(context.Background(), os.Args[2], os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if mode := os.Getenv(processTestChromeMode); mode != "" && processTestChromeArguments(os.Args[1:]) {
		if marker := os.Getenv(processTestChromeMarker); marker != "" {
			if err := os.WriteFile(marker, []byte("started"), 0600); err != nil {
				os.Exit(2)
			}
		}
		if mode == "exit" {
			os.Exit(3)
		}
		for {
			time.Sleep(time.Hour)
		}
	}
	os.Exit(m.Run())
}

func processTestChromeArguments(args []string) bool {
	for _, arg := range args {
		if arg == "--remote-debugging-pipe" {
			return true
		}
	}
	return false
}

func TestProcessRejectsHTMLAndCleansWorkspace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "render")
	r, err := NewProcess(context.Background(), root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = r.Render(context.Background(), []byte(`<script>alert(1)</script>`), DefaultSettings())
	if err == nil {
		t.Fatal("含脚本的输入被接受")
	}
	assertProcessWorkspaceClean(t, root)
}

func TestProcessBrowserStartFailureCleansWorkspace(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(processTestChromeMode, "exit")
	root := filepath.Join(t.TempDir(), "render")
	r, err := NewProcess(context.Background(), root, Options{ChromePath: executable})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, _, err = r.Render(ctx, []byte(`<p>浏览器启动失败</p>`), DefaultSettings()); err == nil {
		t.Fatal("浏览器启动失败未返回错误")
	}
	assertProcessWorkspaceClean(t, root)
}

func TestProcessCancellationWaitsForCleanup(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "chrome-started")
	t.Setenv(processTestChromeMode, "block")
	t.Setenv(processTestChromeMarker, marker)
	root := filepath.Join(t.TempDir(), "render")
	r, err := NewProcess(context.Background(), root, Options{ChromePath: executable})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := r.Render(ctx, []byte(`<p>等待取消</p>`), DefaultSettings())
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("测试浏览器未启动")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消错误=%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后工作进程未退出")
	}
	assertProcessWorkspaceClean(t, root)
}

func TestWorkerProtocolRejectsOversizedAndTruncatedRequests(t *testing.T) {
	var encoded bytes.Buffer
	if err := json.NewEncoder(&encoded).Encode(workerRequest{Document: bytes.Repeat([]byte("x"), 128)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readWorkerRequest(&encoded, 64); err == nil || !strings.Contains(err.Error(), "大小限制") {
		t.Fatalf("超限请求错误=%v", err)
	}
	for _, input := range []string{`{"options":`, `{"document":"YQ`, `{"unexpected":true}`} {
		if _, _, err := readWorkerRequest(strings.NewReader(input), 1024); err == nil {
			t.Fatalf("无效请求被接受: %s", input)
		}
	}
}

func TestWorkerResponseBoundAppliesToStreamCopies(t *testing.T) {
	output := &boundedProtocolOutput{limit: 16}
	if _, err := io.Copy(output, strings.NewReader(strings.Repeat("x", 17))); err == nil {
		t.Fatal("流式复制绕过了响应大小限制")
	}
	if int64(len(output.Bytes())) > output.limit {
		t.Fatal("保留了超限响应")
	}
	if err := decodeProtocol([]byte(`{} {}`), new(workerResponse)); err == nil {
		t.Fatal("多个响应对象被接受")
	}
}

func TestProcessChromeCompletesBeforeWorkspaceCleanup(t *testing.T) {
	if os.Getenv("PRINTKIT_CHROME_TEST") != "1" {
		t.Skip("设置 PRINTKIT_CHROME_TEST=1 后验证实际 Chrome")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := filepath.Join(t.TempDir(), "render")
	r, err := NewProcess(ctx, root, Options{ChromePath: os.Getenv("PRINTKIT_CHROME_PATH")})
	if err != nil {
		t.Fatal(err)
	}
	pdf, engine, err := r.Render(ctx, []byte(`<h1>进程渲染</h1>`), DefaultSettings())
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) || !strings.Contains(engine, "Chrome/") {
		t.Fatalf("产物无效: bytes=%d engine=%q err=%v", len(pdf), engine, err)
	}
	assertProcessWorkspaceClean(t, root)
}

func assertProcessWorkspaceClean(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"tmp", "active"} {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("执行结束后仍有 %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "owner.lock")); err != nil {
		t.Fatalf("执行锁文件未保留: %v", err)
	}
}
