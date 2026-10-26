// Package render 使用独立 Chrome 进程生成静态打印稿的 PDF
package render

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/fetch"
	cdpio "github.com/chromedp/cdproto/io"
	"github.com/chromedp/cdproto/log"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Options 配置浏览器与单份 PDF 的输出上限
type Options struct {
	// ChromePath 为空时由 chromedp 查找已安装的 Chrome
	ChromePath string
	// MaxOutputBytes 为零时使用 50 MiB
	MaxOutputBytes int64
}

// Renderer 为每次调用创建并回收独立浏览器，可被多个调用方并发使用
type Renderer struct {
	opts Options
}

// New 校验配置，不启动或安装浏览器
func New(opts Options) (*Renderer, error) {
	if opts.MaxOutputBytes < 0 {
		return nil, fmt.Errorf("PDF 大小限制不能为负数")
	}
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = 50 << 20
	}
	if opts.ChromePath != "" {
		path, err := exec.LookPath(opts.ChromePath)
		if err != nil {
			return nil, fmt.Errorf("Chrome 路径无效: %w", err)
		}
		opts.ChromePath = path
	}
	return &Renderer{opts: opts}, nil
}

// Render 生成 PDF 并返回 Chrome 版本，ctx 控制启动、资源加载和输出读取的总时限
//
// 输入仅支持 UTF-8 静态 HTML、内联 CSS 和 data URL 资源，脚本或资源失败会使任务失败
// settings 须为通过 Validate 的完整设置，排版采用该值而不补入默认值
// 返回前等待独立浏览器退出并清理临时用户目录
func (r *Renderer) Render(ctx context.Context, input []byte, settings Settings) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if err := settings.Validate(); err != nil {
		return nil, "", fmt.Errorf("排版设置无效: %w", err)
	}
	document, err := prepareHTML(input)
	if err != nil {
		return nil, "", err
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts,
		// 显式保留沙箱，包括调用进程以 root 运行的情况
		chromedp.Flag("no-sandbox", false),
		chromedp.Flag("disable-background-networking", true),
		chromedp.Flag("disable-component-update", true),
	)
	if r.opts.ChromePath != "" {
		opts = append(opts, chromedp.ExecPath(r.opts.ChromePath))
	}
	allocator, cancelAllocator := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAllocator()
	tab, cancelTab := chromedp.NewContext(allocator)
	defer cancelTab()
	version, err := chromedp.CallBrowser(tab, browser.GetVersion, cdp.Empty{})
	if err != nil {
		return nil, "", renderError(ctx, "启动 Chrome", err)
	}
	if err := chromedp.Do(tab, chromedp.Navigate("about:blank")); err != nil {
		return nil, version.Product, renderError(ctx, "初始化页面", err)
	}
	guard := newResourceGuard(tab)
	defer guard.close()
	for _, setup := range []func() error{
		func() error { _, e := chromedp.Call(tab, log.Enable, cdp.Empty{}); return e },
		func() error { _, e := chromedp.Call(tab, network.Enable, network.EnableParams{}); return e },
		func() error { _, e := chromedp.Call(tab, fetch.Enable, fetch.EnableParams{}); return e },
		func() error {
			_, e := chromedp.Call(tab, emulation.SetScriptExecutionDisabled, emulation.SetScriptExecutionDisabledParams{Value: true})
			return e
		},
		func() error {
			_, e := chromedp.Call(tab, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{Media: "print"})
			return e
		},
	} {
		if err := setup(); err != nil {
			return nil, version.Product, renderError(ctx, "配置 Chrome", err)
		}
	}
	frame, err := chromedp.Call(tab, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return nil, version.Product, renderError(ctx, "读取页面", err)
	}
	_, err = chromedp.Call(tab, page.SetDocumentContent, page.SetDocumentContentParams{FrameID: frame.FrameTree.Frame.ID, HTML: document})
	if err != nil {
		return nil, version.Product, renderError(ctx, "载入 HTML", err)
	}
	// 单据脚本已被静态校验和 CSP 拒绝；恢复执行仅供下方受控的异步资源检查
	// 全局禁脚本会同时阻止 Image.decode 与 requestAnimationFrame 的 Promise 回调
	if _, err := chromedp.Call(tab, emulation.SetScriptExecutionDisabled, emulation.SetScriptExecutionDisabledParams{Value: false}); err != nil {
		return nil, version.Product, renderError(ctx, "准备资源检查", err)
	}
	if _, err := chromedp.Run(tab, chromedp.Evaluate[bool](resourcesReady, chromedp.EvalAwaitPromise)); err != nil {
		return nil, version.Product, renderError(ctx, "检查打印资源", err)
	}
	if err := guard.check(tab); err != nil {
		return nil, version.Product, renderError(ctx, "检查浏览器资源", err)
	}
	top, right := settings.MarginMM.Top/25.4, settings.MarginMM.Right/25.4
	bottom, left := settings.MarginMM.Bottom/25.4, settings.MarginMM.Left/25.4
	pdf, err := chromedp.Call(tab, page.PrintToPDF, page.PrintToPDFParams{
		PrintBackground: &settings.PrintBackground, DisplayHeaderFooter: &settings.DisplayHeaderFooter, PreferCSSPageSize: &settings.PreferCSSPageSize,
		PaperWidth: 210 / 25.4, PaperHeight: 297 / 25.4,
		MarginTop: &top, MarginBottom: &bottom, MarginLeft: &left, MarginRight: &right, Scale: settings.Scale,
		TransferMode: page.PrintToPDFTransferModeReturnAsStream,
	})
	if err != nil {
		return nil, version.Product, renderError(ctx, "生成 PDF", err)
	}
	if pdf.Stream == "" {
		return nil, version.Product, fmt.Errorf("Chrome 未返回 PDF 数据流")
	}
	defer func() { _, _ = chromedp.Call(tab, cdpio.Close, cdpio.CloseParams{Handle: pdf.Stream}) }()
	var output bytes.Buffer
	for {
		// 多读一字节即可区分正好达到限制和超限，单次最多接收 64 KiB
		size := int64(64 << 10)
		if remaining := r.opts.MaxOutputBytes - int64(output.Len()); remaining < size {
			size = remaining + 1
		}
		chunk, err := chromedp.Call(tab, cdpio.Read, cdpio.ReadParams{Handle: pdf.Stream, Size: size})
		if err != nil {
			return nil, version.Product, renderError(ctx, "读取 PDF", err)
		}
		if int64(output.Len())+int64(len(chunk.Data)) > r.opts.MaxOutputBytes {
			return nil, version.Product, fmt.Errorf("PDF 产物超过 %d 字节限制", r.opts.MaxOutputBytes)
		}
		_, _ = output.Write(chunk.Data)
		if chunk.EOF {
			break
		}
	}
	if !bytes.HasPrefix(output.Bytes(), []byte("%PDF-")) {
		return nil, version.Product, fmt.Errorf("Chrome 返回的产物不是 PDF")
	}
	if err := guard.check(tab); err != nil {
		return nil, version.Product, renderError(ctx, "检查浏览器资源", err)
	}
	return output.Bytes(), version.Product, nil
}

//go:embed ready.js
var resourcesReady string

func renderError(ctx context.Context, step string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", step, ctx.Err())
	}
	return fmt.Errorf("%s: %w", step, err)
}

type resourceGuard struct {
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	err      error
	barriers chan struct{}
}

const consoleBarrier = "printkit:resources-checked"

func newResourceGuard(tab context.Context) *resourceGuard {
	ctx, cancel := context.WithCancel(tab)
	g := &resourceGuard{cancel: cancel, barriers: make(chan struct{}, 1)}
	paused := chromedp.Events(ctx, fetch.RequestPaused)
	messages := chromedp.Console(ctx)
	g.wg.Add(2)
	go func() {
		defer g.wg.Done()
		for event, err := range paused {
			if err != nil {
				if ctx.Err() == nil {
					g.record(err)
				}
				return
			}
			// data URL 通常不会进入 Fetch；其余请求在发出前全部拒绝
			if strings.HasPrefix(strings.ToLower(event.Request.URL), "data:") {
				_, err = chromedp.Call(ctx, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: event.RequestID})
			} else {
				g.record(errors.New("已阻止外部网络或本地文件资源，请使用 data URL"))
				_, err = chromedp.Call(ctx, fetch.FailRequest, fetch.FailRequestParams{RequestID: event.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
			}
			if err != nil && ctx.Err() == nil {
				g.record(err)
			}
		}
	}()
	go func() {
		defer g.wg.Done()
		for msg, err := range messages {
			if err != nil {
				if ctx.Err() == nil {
					g.record(err)
				}
				return
			}
			if msg.Type == chromedp.ConsoleDebug && msg.Text == consoleBarrier {
				g.barriers <- struct{}{}
				continue
			}
			if msg.Type == chromedp.ConsoleError || msg.Type == chromedp.ConsoleException || (msg.Source == "security" && msg.Type == chromedp.ConsoleWarning) {
				text := msg.Text
				if len(text) > 500 {
					text = text[:500] + "…"
				}
				g.record(fmt.Errorf("浏览器拒绝或加载资源失败: %s", text))
			}
		}
	}()
	return g
}

func (g *resourceGuard) record(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err == nil {
		g.err = err
	}
}

func (g *resourceGuard) check(ctx context.Context) error {
	// 同一事件流中的标记保证此前的浏览器错误已经处理，避免事件消费与返回结果竞态
	if _, err := chromedp.Run(ctx, chromedp.Evaluate[chromedp.Void](`console.debug("`+consoleBarrier+`")`)); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.barriers:
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.err
}

func (g *resourceGuard) close() {
	g.cancel()
	g.wg.Wait()
}
