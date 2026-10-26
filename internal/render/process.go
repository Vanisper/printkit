package render

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// WorkerCommand 是可执行文件用于单次文档渲染的内部命令
const WorkerCommand = "__render-worker"

const (
	processMaxInput  int64 = 100 << 20
	processMaxOutput int64 = 200 << 20
	protocolOverhead int64 = 64 << 10
	workerTimeout          = 5 * time.Minute
)

// ProcessRenderer 在独立工作进程内渲染，调用取消或父进程退出均通过管道关闭通知清理
type ProcessRenderer struct {
	workDir string
	opts    Options
}

// NewProcess 创建进程渲染器，并等待同一工作目录的前次执行清理结束
//
// workDir 的父目录须存在；目录本身及其受管路径不能是符号链接
func NewProcess(ctx context.Context, workDir string, opts Options) (*ProcessRenderer, error) {
	renderer, err := New(opts)
	if err != nil {
		return nil, err
	}
	if renderer.opts.MaxOutputBytes > processMaxOutput {
		return nil, fmt.Errorf("工作进程产物上限不能超过 %d 字节", processMaxOutput)
	}
	root, err := prepareWorkspaceRoot(workDir)
	if err != nil {
		return nil, err
	}
	r := &ProcessRenderer{workDir: root, opts: renderer.opts}
	if _, err := r.call(ctx, workerRequest{Cleanup: true}); err != nil {
		return nil, fmt.Errorf("清理渲染工作目录: %w", err)
	}
	return r, nil
}

// Render 使用完整排版设置生成 PDF，返回前等待工作进程及其浏览器清理结束
func (r *ProcessRenderer) Render(ctx context.Context, input []byte, settings Settings) ([]byte, string, error) {
	if int64(len(input)) > processMaxInput {
		return nil, "", fmt.Errorf("工作进程输入不能超过 %d 字节", processMaxInput)
	}
	if err := settings.Validate(); err != nil {
		return nil, "", fmt.Errorf("排版设置无效: %w", err)
	}
	response, err := r.call(ctx, workerRequest{Document: input, Settings: settings})
	return response.Document, response.Engine, err
}

type workerRequest struct {
	Options          Options  `json:"options"`
	Document         []byte   `json:"document,omitempty"`
	Settings         Settings `json:"settings"`
	Cleanup          bool     `json:"cleanup,omitempty"`
	DeadlineUnixNano int64    `json:"deadline_unix_nano,omitempty"`
}

type workerResponse struct {
	Document []byte `json:"document,omitempty"`
	Engine   string `json:"engine,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (r *ProcessRenderer) call(ctx context.Context, request workerRequest) (workerResponse, error) {
	var response workerResponse
	if err := ctx.Err(); err != nil {
		return response, err
	}
	executable, err := os.Executable()
	if err != nil {
		return response, err
	}
	request.Options = r.opts
	if deadline, ok := ctx.Deadline(); ok {
		request.DeadlineUnixNano = deadline.UnixNano()
	}
	cmd := exec.Command(executable, WorkerCommand, r.workDir)
	input, err := cmd.StdinPipe()
	if err != nil {
		return response, err
	}
	defer input.Close()
	// 保持写端开放作为父进程存活信号，取消时只关管道，让工作进程完成清理
	stop := context.AfterFunc(ctx, func() { _ = input.Close() })
	defer stop()
	output := &boundedProtocolOutput{limit: encodedLimit(r.opts.MaxOutputBytes)}
	cmd.Stdout = output
	// 子进程清理期间不能因父进程 stderr 管道失联而收到 SIGPIPE
	cmd.Stderr = nil
	if err := ctx.Err(); err != nil {
		return response, err
	}
	if err := cmd.Start(); err != nil {
		return response, fmt.Errorf("启动渲染工作进程: %w", err)
	}
	writeErr := json.NewEncoder(input).Encode(request)
	if writeErr != nil {
		_ = input.Close()
	}
	waitErr := cmd.Wait()
	decodeErr := decodeProtocol(output.Bytes(), &response)
	if err := ctx.Err(); err != nil {
		return workerResponse{}, err
	}
	if decodeErr == nil && response.Error != "" {
		return workerResponse{Engine: response.Engine}, errors.New(response.Error)
	}
	if writeErr != nil || waitErr != nil || decodeErr != nil {
		return workerResponse{}, fmt.Errorf("渲染工作进程通信失败: %w", errors.Join(writeErr, waitErr, decodeErr))
	}
	if int64(len(response.Document)) > r.opts.MaxOutputBytes {
		return workerResponse{}, fmt.Errorf("工作进程产物超过大小限制")
	}
	return response, nil
}

// RunWorker 执行单次内部请求，清理浏览器和工作目录后才返回结果
//
// 仅供独立子进程调用；input 的写端必须保持开放，EOF 会取消执行
// 函数会设置该进程的临时目录，调用者应在返回后退出进程
func RunWorker(ctx context.Context, workDir string, input io.Reader, output io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, workerTimeout)
	defer cancel()
	if closer, ok := input.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer stop()
		defer closer.Close()
	}
	response, err := runWorker(ctx, cancel, workDir, input)
	if err != nil {
		response.Document = nil
		response.Error = err.Error()
		if int64(len(response.Error)) > protocolOverhead/2 {
			response.Error = response.Error[:protocolOverhead/2]
		}
	}
	return errors.Join(err, json.NewEncoder(output).Encode(response))
}

func runWorker(ctx context.Context, cancel context.CancelFunc, workDir string, input io.Reader) (response workerResponse, resultErr error) {
	request, decoder, err := readWorkerRequest(input, encodedLimit(processMaxInput))
	if err != nil {
		return response, err
	}
	// 第二个值只用于检测父进程失联或协议尾部，不能等待它才开始渲染
	protocolError := make(chan error, 1)
	go func() {
		var extra any
		err := decoder.Decode(&extra)
		if err == nil {
			protocolError <- errors.New("工作进程只接受一个请求")
		} else if !errors.Is(err, io.EOF) {
			protocolError <- fmt.Errorf("工作进程请求尾部无效: %w", err)
		}
		cancel()
	}()
	if request.DeadlineUnixNano != 0 {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, time.Unix(0, request.DeadlineUnixNano))
		defer stop()
	}
	defer func() {
		select {
		case err := <-protocolError:
			resultErr = errors.Join(resultErr, err)
		default:
		}
	}()
	if request.Options.MaxOutputBytes > processMaxOutput {
		return response, errors.New("工作进程产物上限超过允许范围")
	}
	renderer, err := New(request.Options)
	if err != nil {
		return response, err
	}
	workspace, err := openWorkerWorkspace(ctx, workDir)
	if err != nil {
		return response, err
	}
	removeTemp := true
	defer func() { resultErr = errors.Join(resultErr, workspace.close(removeTemp)) }()
	if request.Cleanup {
		return response, ctx.Err()
	}
	finishProcess, err := enterWorkerProcess()
	if err != nil {
		return response, fmt.Errorf("初始化工作进程资源: %w", err)
	}
	defer func() {
		if err := finishProcess(); err != nil {
			removeTemp = false
			resultErr = errors.Join(resultErr, fmt.Errorf("清理工作进程资源: %w", err))
		} else if err := workspace.markClean(); err != nil {
			removeTemp = false
			resultErr = errors.Join(resultErr, err)
		}
	}()
	if err := workspace.markActive(); err != nil {
		return response, err
	}
	response.Document, response.Engine, resultErr = renderer.Render(ctx, request.Document, request.Settings)
	return response, resultErr
}

func readWorkerRequest(input io.Reader, limit int64) (workerRequest, *json.Decoder, error) {
	limited := &io.LimitedReader{R: input, N: limit + 1}
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	var request workerRequest
	err := decoder.Decode(&request)
	if limited.N <= 0 || int64(len(request.Document)) > processMaxInput {
		return request, decoder, errors.New("工作进程请求超过大小限制")
	}
	if err != nil {
		return request, decoder, fmt.Errorf("读取工作进程请求: %w", err)
	}
	return request, decoder, nil
}

func encodedLimit(documentBytes int64) int64 {
	return (documentBytes+2)/3*4 + protocolOverhead
}

func decodeProtocol(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("工作进程响应只能包含一个 JSON 对象")
	}
	return nil
}

type boundedProtocolOutput struct {
	buffer bytes.Buffer
	limit  int64
}

func (b *boundedProtocolOutput) Write(value []byte) (int, error) {
	if int64(len(value)) > b.limit-int64(b.buffer.Len()) {
		return 0, errors.New("工作进程响应超过大小限制")
	}
	return b.buffer.Write(value)
}

func (b *boundedProtocolOutput) Bytes() []byte {
	return b.buffer.Bytes()
}
