package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Vanisper/printkit/internal/jobs"
)

const acceptanceChromeMarker = "PRINTKIT_ACCEPTANCE_CHROME_MARKER"

func TestMain(m *testing.M) {
	if marker := os.Getenv(acceptanceChromeMarker); marker != "" {
		// 专属子进程阻塞浏览器启动，使 CLI 可以确定性地在 rendering 阶段被终止
		parent := os.Getppid()
		if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			os.Exit(2)
		}
		ticker := time.NewTicker(20 * time.Millisecond)
		deadline := time.NewTimer(45 * time.Second)
		for os.Getppid() == parent {
			select {
			case <-ticker.C:
			case <-deadline.C:
				os.Exit(3)
			}
		}
		// 父进程退出后自行结束，不留下测试子进程或触碰其他 Chrome
		if err := os.WriteFile(marker+".done", []byte("stopped"), 0600); err != nil {
			os.Exit(4)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type acceptanceProcess struct {
	cmd     *exec.Cmd
	done    chan struct{}
	err     error
	logPath string
}

func startAcceptanceCLI(t *testing.T, binary, dir string, environment ...string) *acceptanceProcess {
	t.Helper()
	logFile, err := os.CreateTemp(t.TempDir(), "cli-*.log")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "serve", "--data-dir", dir)
	cmd.Env = append(os.Environ(), environment...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		logFile.Close()
		t.Fatal(err)
	}
	process := &acceptanceProcess{cmd: cmd, done: make(chan struct{}), logPath: logFile.Name()}
	go func() {
		process.err = cmd.Wait()
		logFile.Close()
		close(process.done)
	}()
	t.Cleanup(func() {
		select {
		case <-process.done:
		default:
			_ = cmd.Process.Signal(os.Interrupt)
			select {
			case <-process.done:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-process.done
			}
		}
		if t.Failed() {
			data, _ := os.ReadFile(process.logPath)
			t.Logf("CLI 日志：\n%s", data)
		}
	})
	return process
}

func awaitAcceptance(t *testing.T, ctx context.Context, description string, ready func() bool) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatalf("等待%s：%v", description, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (p *acceptanceProcess) address(t *testing.T, ctx context.Context) string {
	t.Helper()
	var address string
	awaitAcceptance(t, ctx, "CLI 监听地址", func() bool {
		select {
		case <-p.done:
			t.Fatalf("CLI 提前退出：%v", p.err)
		default:
		}
		data, _ := os.ReadFile(p.logPath)
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			var record struct {
				Address string `json:"address"`
			}
			if json.Unmarshal(line, &record) == nil && record.Address != "" {
				address = "http://" + record.Address
				return true
			}
		}
		return false
	})
	return address
}

func acceptanceResponse(t *testing.T, request *http.Request, token string, status int) ([]byte, http.Header) {
	t.Helper()
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != status {
		t.Fatalf("%s %s 返回 %d，期望 %d：%s", request.Method, request.URL.Path, response.StatusCode, status, data)
	}
	return data, response.Header
}

func TestAcceptanceCLIInterruptedRendering(t *testing.T) {
	if os.Getenv("PRINTKIT_ACCEPTANCE_TEST") != "1" {
		t.Skip("设置 PRINTKIT_ACCEPTANCE_TEST=1 后验证独立 CLI 中断恢复")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("此验收使用 Unix 父进程退出语义")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	root := t.TempDir()
	binary := filepath.Join(root, "printkit")
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", binary, ".").CombinedOutput(); err != nil {
		t.Fatalf("构建 CLI：%v\n%s", err, output)
	}
	dir := filepath.Join(root, "data")
	if output, err := exec.CommandContext(ctx, binary, "init", "--data-dir", dir).CombinedOutput(); err != nil {
		t.Fatalf("初始化 CLI：%v\n%s", err, output)
	}
	cfg, err := readConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen = "127.0.0.1:0"
	cfg.ChromePath, err = os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	saveConfig := func() {
		t.Helper()
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	saveConfig()
	marker := filepath.Join(root, "chrome-helper")
	// 强杀后 allocator 无法清理用户目录，由测试持有并回收本次临时资源
	profiles := t.TempDir()
	first := startAcceptanceCLI(t, binary, dir, acceptanceChromeMarker+"="+marker, "TMPDIR="+profiles)
	base := first.address(t, ctx)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("metadata", `{"kind":"html","title":"进程恢复验收"}`); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("document", "acceptance.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, `<html lang="zh-CN"><meta charset="utf-8"><h1>进程恢复验收</h1></html>`); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/jobs", &body)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Idempotency-Key", "cli-restart")
	token := cfg.Callers[0].Token
	data, _ := acceptanceResponse(t, request, token, http.StatusAccepted)
	var job jobs.Job
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	query := func(base string) jobs.Job {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/jobs/"+job.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := acceptanceResponse(t, request, token, http.StatusOK)
		var current jobs.Job
		if err := json.Unmarshal(data, &current); err != nil {
			t.Fatal(err)
		}
		if current.State == jobs.StateFailed {
			t.Fatalf("任务失败：%s", current.Error)
		}
		return current
	}
	awaitAcceptance(t, ctx, "浏览器启动中的 rendering 任务", func() bool {
		_, err := os.Stat(marker)
		return err == nil && query(base).State == jobs.StateRendering
	})
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	case <-ctx.Done():
		t.Fatal("被终止的 CLI 未退出")
	}
	awaitAcceptance(t, ctx, "测试浏览器子进程退出", func() bool {
		_, err := os.Stat(marker + ".done")
		return err == nil
	})
	// 重启后使用真实 Chrome，验证持久化恢复；不验证真实 Chrome 被 SIGKILL 后的回收
	cfg.ChromePath = os.Getenv("PRINTKIT_CHROME_PATH")
	saveConfig()
	second := startAcceptanceCLI(t, binary, dir)
	base = second.address(t, ctx)
	var restored jobs.Job
	awaitAcceptance(t, ctx, "重启后的 PDF 产物", func() bool {
		restored = query(base)
		return restored.State == jobs.StateOutputReady
	})
	if len(restored.Attempts) != 2 || restored.Attempts[0].FinishedAt == nil || !strings.Contains(restored.Attempts[0].Error, "中断") || restored.Attempts[1].Error != "" {
		t.Fatalf("恢复执行记录错误：%+v", restored.Attempts)
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodGet, base+"/v1/jobs/"+job.ID+"/artifact", nil)
	if err != nil {
		t.Fatal(err)
	}
	data, headers := acceptanceResponse(t, request, token, http.StatusOK)
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	if !bytes.HasPrefix(data, []byte("%PDF-")) || digest != restored.ArtifactSHA256 || digest != headers.Get("X-Content-SHA256") {
		t.Fatal("恢复后的 PDF 或摘要不一致")
	}
	t.Logf("独立 CLI 强制终止后恢复同一任务：%s，%d 次执行，%s", job.ID, len(restored.Attempts), restored.Engine)
}
