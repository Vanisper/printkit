package jobs

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Run 恢复中断任务并按受理顺序串行执行，直到上下文取消；每个 Service 只能调用一次
func (s *Service) Run(ctx context.Context) error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("同一任务服务不能重复启动执行器")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.stop, cancel)
	defer stop()
	if err := s.recover(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("恢复中断任务: %w", err)
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		task, err := s.claim(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			select {
			case <-ctx.Done():
				return nil
			case <-s.wake:
			case <-ticker.C:
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("领取任务: %w", err)
		}
		if err = s.execute(ctx, task); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
	}
}

func (s *Service) execute(ctx context.Context, task execution) error {
	execCtx, cancel := context.WithTimeout(ctx, s.opts.ExecutionTimeout)
	defer cancel()
	started := time.Now()
	output, engine, err := s.render(execCtx, task)
	// 服务退出保留 rendering，下一次启动统一记录中断并检查累计恢复上限
	if ctx.Err() != nil {
		return nil
	}
	if execCtx.Err() != nil {
		err = fmt.Errorf("执行超时（%s）: %w", s.opts.ExecutionTimeout, execCtx.Err())
	}
	if err == nil && int64(len(output)) > s.opts.MaxOutputBytes {
		err = fmt.Errorf("%w: 产物为 %d 字节，上限为 %d 字节", ErrTooLarge, len(output), s.opts.MaxOutputBytes)
	}
	if err == nil && !bytes.HasPrefix(output, []byte("%PDF-")) {
		err = errors.New("渲染器未返回 PDF 产物")
	}
	if err == nil {
		if saveErr := s.complete(execCtx, task, output, engine, nil); saveErr == nil {
			s.opts.Logger.Info("任务产物已保存", "job_id", task.id, "bytes", len(output), "duration", time.Since(started))
			return nil
		} else {
			err = fmt.Errorf("保存产物失败: %w", saveErr)
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	// 超时后的失败记录使用服务上下文，避免把失败任务误留为可重试的中断
	failureCtx, failureCancel := context.WithTimeout(ctx, 5*time.Second)
	defer failureCancel()
	if saveErr := s.complete(failureCtx, task, nil, engine, err); saveErr != nil {
		return fmt.Errorf("保存任务失败记录: %w", saveErr)
	}
	s.opts.Logger.Warn("任务执行失败", "job_id", task.id, "error", err, "duration", time.Since(started))
	return nil
}

func (s *Service) render(ctx context.Context, task execution) ([]byte, string, error) {
	if task.kind == KindPDF {
		return task.document, "passthrough", nil
	}
	if s.opts.Renderer == nil {
		return nil, "", errors.New("HTML 渲染器未配置")
	}
	return s.opts.Renderer.Render(ctx, task.document)
}
