// Package jobs 管理可持久恢复的文档产物任务
package jobs

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Vanisper/printkit/internal/render"
)

// Kind 表示提交文档的格式
type Kind string

const (
	KindHTML Kind = "html"
	KindPDF  Kind = "pdf"
)

// State 表示产物任务的当前执行阶段
type State string

const (
	StateQueued    State = "queued"
	StateRendering State = "rendering"
	// StateOutputReady 表示 PDF 已保存且可下载
	StateOutputReady State = "output_ready"
	StateFailed      State = "failed"
)

// 可由调用方分类处理的任务错误
var (
	ErrNotFound = errors.New("任务不存在")
	ErrConflict = errors.New("请求标识已用于不同内容")
	ErrNotReady = errors.New("产物尚未就绪")
	ErrInvalid  = errors.New("任务参数无效")
	ErrTooLarge = errors.New("文档超过大小限制")
)

// Request 包含受理任务所需的完整文档，调用方身份由接入端提供
type Request struct {
	// CallerID 为接入端认证后的调用方标识，最多 128 字节
	CallerID string
	// Key 在调用方范围内唯一，最多 128 字节；复用时格式、标题和文档必须相同
	Key  string
	Kind Kind
	// Title 可为空，最多 256 字节且不能包含控制字符
	Title string
	// Document 为完整文档；PDF 只检查文件头，不保证可解析或可打印
	Document []byte
}

// Attempt 保留每次执行及中断的结果
type Attempt struct {
	Number     int        `json:"number"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

// Job 是不含文档字节的任务视图
type Job struct {
	ID             string    `json:"id"`
	Kind           Kind      `json:"kind"`
	Title          string    `json:"title"`
	Mode           string    `json:"mode"`
	State          State     `json:"state"`
	InputSHA256    string    `json:"input_sha256"`
	ArtifactSHA256 string    `json:"artifact_sha256,omitempty"`
	OutputSize     int64     `json:"output_size,omitempty"`
	Engine         string    `json:"engine,omitempty"`
	Error          string    `json:"error,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Attempts       []Attempt `json:"attempts"`
}

// Renderer 将完整静态 HTML 转为 PDF，并返回渲染器版本
//
// 实现必须响应上下文取消，并在返回前清理所持有的浏览器进程与临时资源
type Renderer interface {
	// Render 使用任务保存的完整排版设置，不以当前默认值替换
	Render(ctx context.Context, document []byte, settings render.Settings) ([]byte, string, error)
}

// Options 配置单个任务执行器，零值限制采用默认值
type Options struct {
	MaxInputBytes  int64
	MaxOutputBytes int64
	// ExecutionTimeout 覆盖文档渲染及成功产物的持久化，超时后的失败记录单独保存
	ExecutionTimeout time.Duration
	Renderer         Renderer
	Logger           *slog.Logger
}

const (
	DefaultMaxInputBytes    int64 = 20 << 20
	DefaultMaxOutputBytes   int64 = 50 << 20
	DefaultExecutionTimeout       = 60 * time.Second
)
