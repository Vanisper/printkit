package render

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

func TestWorkspaceStartupRemovesOnlyManagedResidue(t *testing.T) {
	for _, marker := range []string{"", "clean\n"} {
		name := "no-marker"
		if marker != "" {
			name = "clean-marker"
		}
		t.Run(name, func(t *testing.T) {
			root := workspaceTestRoot(t)
			writeWorkspaceTestFile(t, filepath.Join(root, "tmp", "profile", "residue"), "old profile")
			unrelated := filepath.Join(root, "operator-note")
			writeWorkspaceTestFile(t, unrelated, "keep")
			lockPath := filepath.Join(root, "owner.lock")
			writeWorkspaceTestFile(t, lockPath, "")
			lockBefore, err := os.Stat(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			if marker != "" {
				writeWorkspaceTestFile(t, filepath.Join(root, "active"), marker)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := NewProcess(ctx, root, Options{}); err != nil {
				t.Fatal(err)
			}
			assertProcessWorkspaceClean(t, root)
			assertWorkspaceTestContents(t, unrelated, "keep")
			lockAfter, err := os.Stat(lockPath)
			if err != nil || !os.SameFile(lockBefore, lockAfter) {
				t.Fatalf("启动清理替换了执行锁: %v", err)
			}
		})
	}
}

func TestWorkspaceActiveMarkerPreservesResidue(t *testing.T) {
	root := workspaceTestRoot(t)
	residue := filepath.Join(root, "tmp", "profile", "residue")
	marker := filepath.Join(root, "active")
	writeWorkspaceTestFile(t, residue, "possibly active profile")
	writeWorkspaceTestFile(t, marker, "active\n")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := NewProcess(ctx, root, Options{}); err == nil {
		t.Fatal("未确认浏览器退出时仍接受了工作目录")
	}
	assertWorkspaceTestContents(t, residue, "possibly active profile")
	assertWorkspaceTestContents(t, marker, "active\n")
}

func TestWorkspaceRejectsSymlinksWithoutChangingTargets(t *testing.T) {
	for _, component := range []string{"root", "tmp", "owner.lock", "active"} {
		t.Run(component, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "render")
			outside := filepath.Join(parent, "outside")
			sentinel := filepath.Join(outside, "sentinel")
			writeWorkspaceTestFile(t, sentinel, "keep outside")
			var linkPath, target string
			if component == "root" {
				linkPath, target = root, outside
			} else {
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
				linkPath = filepath.Join(root, component)
				target = sentinel
				if component == "tmp" {
					target = outside
				}
			}
			if err := os.Symlink(target, linkPath); err != nil {
				if runtime.GOOS == "windows" {
					t.Skipf("当前 Windows 用户不能创建符号链接: %v", err)
				}
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := NewProcess(ctx, root, Options{}); err == nil {
				t.Fatalf("接受了符号链接路径 %s", component)
			}
			assertWorkspaceTestContents(t, sentinel, "keep outside")
			if actual, err := os.Readlink(linkPath); err != nil || actual != target {
				t.Fatalf("拒绝路径时修改了符号链接: target=%q err=%v", actual, err)
			}
		})
	}
}

func TestWorkspaceWaitingForOwnerCanBeCanceled(t *testing.T) {
	root := workspaceTestRoot(t)
	residue := filepath.Join(root, "tmp", "profile", "residue")
	writeWorkspaceTestFile(t, residue, "owner still active")
	lock := flock.New(filepath.Join(root, "owner.lock"), flock.SetPermissions(0600))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := lock.Close(); err != nil {
			t.Errorf("关闭测试执行锁: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, err := NewProcess(ctx, root, Options{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待执行锁时取消错误=%v", err)
	}
	assertWorkspaceTestContents(t, residue, "owner still active")
}

func workspaceTestRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "render")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeWorkspaceTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func assertWorkspaceTestContents(t *testing.T, path, expected string) {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil || string(value) != expected {
		t.Fatalf("文件内容改变: path=%s value=%q err=%v", path, value, err)
	}
}
