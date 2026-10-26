//go:build darwin || linux

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
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Vanisper/printkit/internal/jobs"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
)

type acceptanceChromeProcess struct {
	pid     int
	parent  int
	started string
	command string
}

func (p acceptanceChromeProcess) sameIdentity(other acceptanceChromeProcess) bool {
	return p.pid == other.pid && p.started == other.started && p.command == other.command
}

func acceptanceProcessSnapshot(t *testing.T) map[int]acceptanceChromeProcess {
	t.Helper()
	output, err := exec.Command("ps", "-ax", "-o", "pid=,ppid=,stat=,lstart=,command=").Output()
	if err != nil {
		t.Fatal(err)
	}
	processes := make(map[int]acceptanceChromeProcess)
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 9 || strings.Contains(fields[2], "Z") {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			t.Fatal(err)
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatal(err)
		}
		processes[pid] = acceptanceChromeProcess{pid: pid, parent: parent, started: strings.Join(fields[3:8], " "), command: strings.Join(fields[8:], " ")}
	}
	return processes
}

func observeAcceptanceChrome(t *testing.T, profile string, observed map[int]acceptanceChromeProcess) []acceptanceChromeProcess {
	t.Helper()
	snapshot := acceptanceProcessSnapshot(t)
	live := make(map[int]acceptanceChromeProcess)
	for pid, process := range snapshot {
		previous, known := observed[pid]
		if strings.Contains(process.command+" ", "--user-data-dir="+profile+" ") || (known && previous.sameIdentity(process)) {
			live[pid] = process
		}
	}
	// 同时跟踪进程树和精确 profile，保留不携带 profile 参数的后代以及父进程退出后的身份
	for {
		count := len(live)
		for pid, process := range snapshot {
			if _, belongs := live[process.parent]; belongs {
				live[pid] = process
			}
		}
		if len(live) == count {
			break
		}
	}
	processes := make([]acceptanceChromeProcess, 0, len(live))
	for pid, process := range live {
		observed[pid] = process
		processes = append(processes, process)
	}
	return processes
}

func cleanupAcceptanceChrome(t *testing.T, profile string, observed map[int]acceptanceChromeProcess) {
	t.Helper()
	for _, process := range observeAcceptanceChrome(t, profile, observed) {
		_ = syscall.Kill(process.pid, syscall.SIGKILL)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	awaitAcceptance(t, ctx, "本测试浏览器清理", func() bool {
		return len(observeAcceptanceChrome(t, profile, observed)) == 0
	})
}

func TestAcceptanceCLIReclaimsChromeAfterKill(t *testing.T) {
	if os.Getenv("PRINTKIT_ACCEPTANCE_TEST") != "1" {
		t.Skip("设置 PRINTKIT_ACCEPTANCE_TEST=1 后验证真实 Chrome 强制退出回收")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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
	cfg.Listen, cfg.ChromePath = "127.0.0.1:0", os.Getenv("PRINTKIT_CHROME_PATH")
	cfg.ExecutionTimeoutSeconds = 90
	configData, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), configData, 0600); err != nil {
		t.Fatal(err)
	}

	// 独立浏览器由测试持有，服务的回收范围不能扩展到它的进程或用户目录
	sentinelProfile := filepath.Join(root, "sentinel")
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.Flag("no-sandbox", false), chromedp.UserDataDir(sentinelProfile))
	if cfg.ChromePath != "" {
		opts = append(opts, chromedp.ExecPath(cfg.ChromePath))
	}
	allocator, cancelAllocator := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAllocator()
	sentinel, cancelSentinel := chromedp.NewContext(allocator)
	defer cancelSentinel()
	sentinelProcesses := make(map[int]acceptanceChromeProcess)
	t.Cleanup(func() {
		cleanupAcceptanceChrome(t, sentinelProfile, sentinelProcesses)
	})
	version, err := chromedp.CallBrowser(sentinel, browser.GetVersion, cdp.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	sentinelPID := chromedp.FromContext(sentinel).Browser.Process().Pid
	observeAcceptanceChrome(t, sentinelProfile, sentinelProcesses)
	if _, exists := sentinelProcesses[sentinelPID]; !exists {
		t.Fatal("未能捕获独立浏览器进程树")
	}

	first := startAcceptanceCLI(t, binary, dir)
	base := first.address(t, ctx)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("metadata", `{"kind":"html","title":"浏览器回收验收"}`); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("document", "lifecycle.html")
	if err != nil {
		t.Fatal(err)
	}
	// 足够多的静态节点使浏览器进入真实排版，便于在产物完成前观察并挂起进程
	document := `<html lang="zh-CN"><meta charset="utf-8"><style>body{font:9px sans-serif}</style>` + strings.Repeat("<span>生命周期验收样例 </span>", 9000) + `</html>`
	if _, err := io.WriteString(file, document); err != nil {
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
	request.Header.Set("Idempotency-Key", "chrome-lifecycle")
	token := cfg.Callers[0].Token
	data, _ := acceptanceResponse(t, request, token, http.StatusAccepted)
	var job jobs.Job
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	query := func(address string) jobs.Job {
		t.Helper()
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, address+"/v1/jobs/"+job.ID, nil)
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
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	managedRoot := filepath.Join(canonicalDir, "render", "tmp")
	var profile string
	var mainChrome acceptanceChromeProcess
	observed := make(map[int]acceptanceChromeProcess)
	awaitAcceptance(t, ctx, "真实 Chrome 与页面进程", func() bool {
		profiles, err := filepath.Glob(filepath.Join(managedRoot, "chromedp-runner*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range profiles {
			processes := observeAcceptanceChrome(t, candidate, observed)
			var main acceptanceChromeProcess
			hasRenderer := false
			for _, process := range processes {
				if strings.Contains(process.command, "--type=renderer") {
					hasRenderer = true
				}
				if strings.Contains(process.command, "--remote-debugging-pipe") && !strings.Contains(process.command, "--type=") {
					main = process
				}
			}
			if main.pid != 0 && hasRenderer {
				profile, mainChrome = candidate, main
				return true
			}
		}
		if state := query(base).State; state != jobs.StateQueued && state != jobs.StateRendering {
			t.Fatal("未观察到真实 Chrome 前任务已结束")
		}
		return false
	})
	// 失败清理仅操作身份仍一致的已记录进程树，以及该 profile 下的新生进程
	t.Cleanup(func() {
		cleanupAcceptanceChrome(t, profile, observed)
	})
	stopped := false
	for _, process := range observeAcceptanceChrome(t, profile, observed) {
		if process.sameIdentity(mainChrome) {
			if err := syscall.Kill(process.pid, syscall.SIGSTOP); err != nil {
				t.Fatal(err)
			}
			stopped = true
			break
		}
	}
	if !stopped {
		t.Fatal("挂起前 Chrome 进程已退出或被替换")
	}
	if current := query(base); current.State != jobs.StateRendering {
		t.Fatalf("挂起 Chrome 后任务不在 rendering：%s", current.State)
	}
	if err := first.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.done:
	case <-ctx.Done():
		t.Fatal("被强制终止的 CLI 未退出")
	}
	// 立即重启，旧浏览器的清理与新服务获取渲染执行权可以并发发生
	cleanupCtx, stopCleanup := context.WithTimeout(ctx, 10*time.Second)
	defer stopCleanup()
	second := startAcceptanceCLI(t, binary, dir)
	base = second.address(t, cleanupCtx)
	awaitAcceptance(t, cleanupCtx, "旧 Chrome 进程与目录回收", func() bool {
		_, err := os.Stat(profile)
		return os.IsNotExist(err) && len(observeAcceptanceChrome(t, profile, observed)) == 0
	})
	var restored jobs.Job
	awaitAcceptance(t, ctx, "中断任务恢复产物", func() bool {
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
	if chromedp.FromContext(sentinel).Browser.Process().Pid != sentinelPID {
		t.Fatal("独立浏览器进程发生变化")
	}
	observeAcceptanceChrome(t, sentinelProfile, sentinelProcesses)
	if current, err := chromedp.CallBrowser(sentinel, browser.GetVersion, cdp.Empty{}); err != nil || current.Product != version.Product {
		t.Fatalf("独立浏览器受到影响：%v", err)
	}
	if _, err := os.Stat(sentinelProfile); err != nil {
		t.Fatalf("独立浏览器目录受到影响：%v", err)
	}
	profiles, err := filepath.Glob(filepath.Join(managedRoot, "chromedp-runner*"))
	if err != nil || len(profiles) != 0 {
		t.Fatalf("任务完成后仍有受管浏览器目录：%v，%v", profiles, err)
	}
	t.Logf("Chrome %d 被挂起后强杀服务，旧进程与目录已回收，任务 %s 恢复，独立 Chrome %d 保持运行", mainChrome.pid, job.ID, sentinelPID)
}
