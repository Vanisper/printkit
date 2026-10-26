//go:build windows

package render

import (
	"errors"
	"fmt"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// enterWorkerProcess 必须在独立 worker 内、启动 Chrome 前调用
//
// 清理失败时保留 Job 句柄，调用方须保留工作目录并退出，让内核终止剩余子进程
func enterWorkerProcess() (func() error, error) {
	// 未命名且不继承的句柄只由 worker 持有，worker 异常退出也会触发整组回收
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("创建 Windows 渲染进程组: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, errors.Join(fmt.Errorf("配置 Windows 渲染进程组: %w", err), windows.CloseHandle(job))
	}
	if err = windows.AssignProcessToJobObject(job, windows.CurrentProcess()); err != nil {
		return nil, errors.Join(fmt.Errorf("加入 Windows 渲染进程组: %w", err), windows.CloseHandle(job))
	}

	return sync.OnceValue(func() error {
		if err := awaitWorkerChildren(job); err != nil {
			return err
		}
		// worker 自身也在 Job 中，只剩自身后才能解除关闭即终止，继续返回结果与清理目录
		limits.BasicLimitInformation.LimitFlags = 0
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
			return fmt.Errorf("解除 Windows 渲染进程组退出保护: %w", err)
		}
		if err := windows.CloseHandle(job); err != nil {
			return fmt.Errorf("关闭 Windows 渲染进程组: %w", err)
		}
		return nil
	}), nil
}

func awaitWorkerChildren(job windows.Handle) error {
	deadline := time.Now().Add(2 * time.Second)
	for {
		var accounting jobBasicAccounting
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil {
			return fmt.Errorf("读取 Windows 渲染进程组状态: %w", err)
		}
		if accounting.ActiveProcesses == 1 {
			return nil
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("Windows 渲染进程组未清理完毕，活动进程数为 %d，工作目录已保留", accounting.ActiveProcesses)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// jobBasicAccounting 对应 Windows JOBOBJECT_BASIC_ACCOUNTING_INFORMATION 的固定布局
type jobBasicAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}
