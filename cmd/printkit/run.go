package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/Vanisper/printkit/internal/httpapi"
	"github.com/Vanisper/printkit/internal/jobs"
	"github.com/Vanisper/printkit/internal/render"
	"github.com/gofrs/flock"
)

func run(ctx context.Context, args []string, out, errOut io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Fprintln(out, "用法：printkit <init|serve|token|version> [--data-dir 目录]\n  init     创建本地配置和调用令牌\n  serve    启动文档产物服务\n  token    输出指定调用方的令牌（默认 demo）\n  version  显示构建版本")
		return nil
	}
	if args[0] == "version" {
		fmt.Fprintln(out, version)
		return nil
	}
	if args[0] != "init" && args[0] != "serve" && args[0] != "token" {
		return fmt.Errorf("未知命令 %q", args[0])
	}
	defaultDir, err := defaultDataDir()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(errOut)
	dataDir := flags.String("data-dir", defaultDir, "配置和任务数据库目录")
	callerID := "demo"
	if args[0] == "token" {
		flags.StringVar(&callerID, "caller", callerID, "调用方标识")
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("不接受额外的位置参数")
	}
	dir, err := filepath.Abs(*dataDir)
	if err != nil {
		return err
	}
	if args[0] == "init" {
		if err := initialize(dir); err != nil {
			return err
		}
		fmt.Fprintf(out, "已创建 %s\n使用 token 命令读取调用令牌，使用 serve 命令启动\n", filepath.Join(dir, "config.json"))
		return nil
	}
	cfg, err := readConfig(dir)
	if err != nil {
		return err
	}
	if args[0] == "token" {
		for _, caller := range cfg.Callers {
			if caller.ID == callerID {
				_, err := fmt.Fprintln(out, caller.Token)
				return err
			}
		}
		return errors.New("调用方不存在")
	}
	return serve(ctx, dir, cfg, errOut)
}

func serve(ctx context.Context, dir string, cfg config, errOut io.Writer) error {
	if err := checkDataDir(dir); err != nil {
		return err
	}
	// 进程锁随异常退出释放，避免第二个执行器误恢复仍在运行的任务
	lock := flock.New(filepath.Join(dir, "instance.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("此数据目录已有运行中的 printkit")
	}
	defer lock.Close()

	renderer, err := render.New(render.Options{ChromePath: cfg.ChromePath, MaxOutputBytes: cfg.MaxOutputBytes})
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(errOut, nil))
	service, err := jobs.Open(ctx, filepath.Join(dir, "jobs.db"), jobs.Options{
		MaxInputBytes: cfg.MaxInputBytes, MaxOutputBytes: cfg.MaxOutputBytes,
		ExecutionTimeout: time.Duration(cfg.ExecutionTimeoutSeconds) * time.Second,
		Renderer:         renderer, Logger: logger,
	})
	if err != nil {
		return err
	}
	defer service.Close()
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("监听本机端口：%w", err)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	callers := make([]httpapi.Caller, 0, len(cfg.Callers))
	for _, caller := range cfg.Callers {
		callers = append(callers, httpapi.Caller{ID: caller.ID, Token: caller.Token, Origins: caller.Origins})
	}
	handler, err := httpapi.New(service, httpapi.Options{
		Callers: callers, MaxInputBytes: cfg.MaxInputBytes,
		AllowedHosts: []string{listener.Addr().String(), net.JoinHostPort("localhost", port)},
		Logger:       logger,
	})
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	workerDone := make(chan error, 1)
	serverDone := make(chan error, 1)
	go func() { workerDone <- service.Run(workerCtx) }()
	go func() { serverDone <- server.Serve(listener) }()
	logger.Info("文档产物服务已启动", "address", listener.Addr().String(), "demo", "http://"+listener.Addr().String()+"/demo/", "mode", "artifact", "version", version)
	var runErr error
	workerExited := false
	select {
	case <-ctx.Done():
	case runErr = <-workerDone:
		workerExited = true
	case runErr = <-serverDone:
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, http.ErrServerClosed) {
		runErr = nil
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil {
		_ = server.Close()
	}
	stopWorker()
	if !workerExited {
		workerErr := <-workerDone
		if !errors.Is(workerErr, context.Canceled) {
			runErr = errors.Join(runErr, workerErr)
		}
	}
	return errors.Join(runErr, shutdownErr)
}
