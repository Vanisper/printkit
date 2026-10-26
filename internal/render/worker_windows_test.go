//go:build windows

package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const workerJobTestMode = "PRINTKIT_RENDER_JOB_TEST_MODE"

type workerJobTestMessage struct {
	PID   int    `json:"pid,omitempty"`
	Error string `json:"error,omitempty"`
}

func TestWorkerProcessJobLifecycle(t *testing.T) {
	for _, mode := range []string{"clean", "remaining-child", "killed-worker"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(ctx, binary, "-test.run=^TestWorkerProcessJobHelper$")
			cmd.Env = append(os.Environ(), workerJobTestMode+"="+mode)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			decoder := json.NewDecoder(stdout)
			var message workerJobTestMessage
			if err := decoder.Decode(&message); err != nil {
				t.Fatalf("读取 worker 状态: %v", err)
			}
			if mode == "clean" {
				if message.Error != "" {
					t.Fatal(message.Error)
				}
				if err := cmd.Wait(); err != nil {
					t.Fatalf("正常清理终止了 worker: %v\n%s", err, stderr.String())
				}
				return
			}
			// 在 worker 退出前取得稳定进程句柄，后续检查不依赖可能复用的 PID
			child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(message.PID))
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = windows.TerminateProcess(child, 1)
				_ = windows.CloseHandle(child)
			}()
			if mode == "killed-worker" {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err == nil {
					t.Fatal("被终止的 worker 不应正常退出")
				}
			} else {
				if _, err := stdin.Write([]byte{1}); err != nil {
					t.Fatal(err)
				}
				if err := decoder.Decode(&message); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(message.Error, "未清理完毕") {
					t.Fatalf("剩余子进程未报告清理失败：%q", message.Error)
				}
				if err := cmd.Wait(); err != nil {
					var exitErr *exec.ExitError
					if ctx.Err() != nil || !errors.As(err, &exitErr) {
						t.Fatalf("worker 返回清理失败后未退出: %v\n%s", err, stderr.String())
					}
				}
			}
			state, err := windows.WaitForSingleObject(child, 5000)
			if err != nil || state != windows.WAIT_OBJECT_0 {
				t.Fatalf("worker 退出后仍有存活子进程：state=%d err=%v", state, err)
			}
		})
	}
}

func TestWorkerProcessJobHelper(t *testing.T) {
	mode := os.Getenv(workerJobTestMode)
	if mode == "" {
		t.Skip("仅由进程隔离测试调用")
	}
	if mode == "child" {
		time.Sleep(time.Minute)
		return
	}
	cleanup, err := enterWorkerProcess()
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	if mode == "clean" {
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
		if err := cleanup(); err != nil {
			t.Fatal(err)
		}
		if err := encoder.Encode(workerJobTestMessage{}); err != nil {
			t.Fatal(err)
		}
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(binary, "-test.run=^TestWorkerProcessJobHelper$")
	child.Env = append(os.Environ(), workerJobTestMode+"=child")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Encode(workerJobTestMessage{PID: child.Process.Pid}); err != nil {
		t.Fatal(err)
	}
	var ready [1]byte
	if _, err := io.ReadFull(os.Stdin, ready[:]); err != nil {
		t.Fatal(err)
	}
	if err := cleanup(); err == nil {
		t.Fatal("子进程仍存活时清理不应成功")
	} else if err := encoder.Encode(workerJobTestMessage{Error: err.Error()}); err != nil {
		t.Fatal(err)
	}
}
