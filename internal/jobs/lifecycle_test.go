package jobs

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCloseWaitsForRendererCleanup(t *testing.T) {
	entered, cleaning, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	s := openTestService(t, "", Options{Renderer: renderFunc(func(ctx context.Context, _ []byte) ([]byte, string, error) {
		close(entered)
		<-ctx.Done()
		close(cleaning)
		<-release
		return nil, "fake", ctx.Err()
	})})
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	submitTest(t, s, Request{CallerID: "demo", Key: "close", Kind: KindHTML, Document: []byte("<p>退出</p>")})
	runDone := make(chan error, 1)
	go func() { runDone <- s.Run(context.Background()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("渲染器未启动")
	}
	const closers = 4
	closed := make(chan error, closers)
	for range closers {
		go func() { closed <- s.Close() }()
	}
	select {
	case <-cleaning:
	case <-time.After(5 * time.Second):
		t.Fatal("关闭未取消执行")
	}
	select {
	case err := <-closed:
		t.Fatalf("清理尚未完成，Close 已返回：%v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := s.Run(context.Background()); err == nil {
		t.Error("关闭中仍可启动执行器")
	}
	releaseOnce.Do(func() { close(release) })
	for range closers {
		select {
		case err := <-closed:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("渲染器清理后 Close 未返回")
		}
	}
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
}

func TestCloseBeforeRunRejectsExecution(t *testing.T) {
	s := openTestService(t, "", Options{})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "已关闭") {
		t.Fatalf("关闭后启动的结果错误：%v", err)
	}
}

func TestRunAndCloseCanRace(t *testing.T) {
	for range 32 {
		s := openTestService(t, "", Options{})
		start := make(chan struct{})
		runDone, closeDone := make(chan error, 1), make(chan error, 1)
		go func() { <-start; runDone <- s.Run(context.Background()) }()
		go func() { <-start; closeDone <- s.Close() }()
		close(start)
		select {
		case err := <-runDone:
			if err != nil && !strings.Contains(err.Error(), "已关闭") {
				t.Fatalf("同时启动与关闭时出现存储或执行错误：%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("执行器未退出")
		}
		select {
		case err := <-closeDone:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("关闭被阻塞")
		}
	}
}
