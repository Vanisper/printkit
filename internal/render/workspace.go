package render

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/gofrs/flock"
)

type workerWorkspace struct {
	root   string
	tmp    string
	info   fs.FileInfo
	files  *os.Root
	lock   *flock.Flock
	env    []workerEnv
	active bool
}

type workerEnv struct {
	name, value string
	present     bool
}

func prepareWorkspaceRoot(path string) (string, error) {
	if path == "" {
		return "", errors.New("渲染工作目录不能为空")
	}
	root, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	// 解析系统目录的别名，但不允许受管根目录本身指向另一个目录
	parent, err := filepath.EvalSymlinks(filepath.Dir(root))
	if err != nil {
		return "", err
	}
	root = filepath.Join(parent, filepath.Base(root))
	if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	info, err := directoryInfo(root)
	if err != nil {
		return "", err
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return "", errors.New("渲染工作目录不能允许其他用户访问")
	}
	return root, nil
}

func openWorkerWorkspace(ctx context.Context, path string) (*workerWorkspace, error) {
	root, err := prepareWorkspaceRoot(path)
	if err != nil {
		return nil, err
	}
	info, err := directoryInfo(root)
	if err != nil {
		return nil, err
	}
	files, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	openedInfo, err := files.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = files.Close()
		return nil, errors.New("渲染工作目录在打开期间发生变化")
	}
	lockPath := filepath.Join(root, "owner.lock")
	if info, err := files.Lstat("owner.lock"); err == nil && !info.Mode().IsRegular() {
		_ = files.Close()
		return nil, errors.New("渲染执行锁必须是普通文件")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = files.Close()
		return nil, err
	}
	lock := flock.New(lockPath, flock.SetFlag(os.O_CREATE|os.O_RDWR), flock.SetPermissions(0600))
	locked, err := lock.TryLockContext(ctx, 20*time.Millisecond)
	if err != nil || !locked {
		_ = lock.Close()
		_ = files.Close()
		return nil, fmt.Errorf("等待渲染执行锁: %w", errors.Join(err, ctx.Err()))
	}
	w := &workerWorkspace{root: root, tmp: filepath.Join(root, "tmp"), info: info, files: files, lock: lock}
	if err := w.prepare(); err != nil {
		return nil, errors.Join(err, w.close(false))
	}
	return w, nil
}

func (w *workerWorkspace) prepare() error {
	if err := w.checkRoot(); err != nil {
		return err
	}
	lockedInfo, err := w.lock.Stat()
	if err != nil {
		return err
	}
	pathInfo, err := w.files.Lstat("owner.lock")
	if err != nil || !pathInfo.Mode().IsRegular() || !os.SameFile(lockedInfo, pathInfo) {
		return errors.New("渲染执行锁路径已改变")
	}
	if err := w.checkMarker(); err != nil {
		return err
	}
	if err := w.removeTemp(); err != nil {
		return err
	}
	if err := w.removeMarker(); err != nil {
		return err
	}
	if err := w.files.Mkdir("tmp", 0700); err != nil {
		return err
	}
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		value, present := os.LookupEnv(name)
		w.env = append(w.env, workerEnv{name: name, value: value, present: present})
		if err := os.Setenv(name, w.tmp); err != nil {
			return err
		}
	}
	actual, err := os.Stat(os.TempDir())
	if err != nil {
		return err
	}
	expected, err := w.files.Stat("tmp")
	if err != nil || !os.SameFile(actual, expected) {
		return errors.New("操作系统临时目录未指向渲染工作目录")
	}
	return nil
}

func (w *workerWorkspace) checkRoot() error {
	current, err := directoryInfo(w.root)
	if err != nil {
		return err
	}
	if !os.SameFile(w.info, current) {
		return errors.New("渲染工作目录在执行期间发生变化")
	}
	return nil
}

func (w *workerWorkspace) removeTemp() error {
	if err := w.checkRoot(); err != nil {
		return err
	}
	info, err := w.files.Lstat("tmp")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("渲染临时路径必须是普通目录")
	}
	if err := w.files.RemoveAll("tmp"); err != nil {
		return fmt.Errorf("删除渲染临时目录: %w", err)
	}
	return nil
}

func (w *workerWorkspace) close(removeTemp bool) error {
	var err error
	if removeTemp {
		err = w.removeTemp()
		if err == nil {
			err = w.removeMarker()
		}
	}
	for _, env := range w.env {
		if env.present {
			err = errors.Join(err, os.Setenv(env.name, env.value))
		} else {
			err = errors.Join(err, os.Unsetenv(env.name))
		}
	}
	return errors.Join(err, w.lock.Close(), w.files.Close())
}

func (w *workerWorkspace) checkMarker() error {
	info, err := w.files.Lstat("active")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 16 {
		return errors.New("渲染活动标记无效，已保留工作目录")
	}
	value, err := w.files.ReadFile("active")
	if err != nil {
		return err
	}
	if string(value) != "clean\n" {
		return errors.New("前次渲染工作进程异常退出，无法确认浏览器已回收；已保留工作目录")
	}
	return nil
}

func (w *workerWorkspace) markActive() error {
	if err := w.checkRoot(); err != nil {
		return err
	}
	f, err := w.files.OpenFile("active", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	w.active = true
	return writeMarker(f, "active\n")
}

func (w *workerWorkspace) markClean() error {
	if !w.active {
		return nil
	}
	if err := w.checkRoot(); err != nil {
		return err
	}
	info, err := w.files.Lstat("active")
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("渲染活动标记在执行期间发生变化")
	}
	f, err := w.files.OpenFile("active", os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	return writeMarker(f, "clean\n")
}

func (w *workerWorkspace) removeMarker() error {
	if err := w.checkRoot(); err != nil {
		return err
	}
	err := w.files.Remove("active")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func writeMarker(f *os.File, value string) error {
	_, writeErr := f.WriteString(value)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}

func directoryInfo(path string) (fs.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("受管路径必须是普通目录: %s", path)
	}
	return info, nil
}
