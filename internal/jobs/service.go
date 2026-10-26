package jobs

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Vanisper/printkit/internal/render"
	_ "modernc.org/sqlite"
)

// Service 持久保存任务及产物，受理请求取消不会取消已经提交的任务
type Service struct {
	db        *sql.DB
	opts      Options
	wake      chan struct{}
	stop      context.Context
	cancel    context.CancelFunc
	lifecycle sync.Mutex
	runDone   chan struct{}
	closed    bool
	close     sync.Once
	closeErr  error
}

// Open 打开版本化 SQLite 存储，零值限制使用默认值
//
// 调用方须在打开前取得数据库的独占执行权，并持有到 Close 返回
// ctx 只控制存储初始化，执行生命周期由 Run 的上下文和 Close 控制
func Open(ctx context.Context, path string, opts Options) (*Service, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: 数据库路径为空", ErrInvalid)
	}
	if opts.MaxInputBytes < 0 || opts.MaxOutputBytes < 0 || opts.ExecutionTimeout < 0 {
		return nil, fmt.Errorf("%w: 大小和超时限制不能为负数", ErrInvalid)
	}
	if opts.MaxInputBytes == 0 {
		opts.MaxInputBytes = DefaultMaxInputBytes
	}
	if opts.MaxOutputBytes == 0 {
		opts.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if opts.ExecutionTimeout == 0 {
		opts.ExecutionTimeout = DefaultExecutionTimeout
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开任务库: %w", err)
	}
	// 单连接使去重、领取和状态更新按事务串行执行，渲染期间不占用连接
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err = initialize(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err = os.Chmod(path, 0600); err != nil {
			db.Close()
			return nil, fmt.Errorf("设置任务库权限: %w", err)
		}
	}
	stop, cancel := context.WithCancel(context.Background())
	return &Service{db: db, opts: opts, wake: make(chan struct{}, 1), stop: stop, cancel: cancel}, nil
}

// Close 取消并等待已启动的 Run 完成资源清理，再关闭存储，可并发重复调用
//
// 关闭开始后拒绝新的 Run；正在执行的任务留待下次启动恢复
func (s *Service) Close() error {
	s.close.Do(func() {
		s.lifecycle.Lock()
		s.closed = true
		done := s.runDone
		s.lifecycle.Unlock()
		s.cancel()
		if done != nil {
			<-done
		}
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

// Submit 在输入和任务原子保存后返回；相同调用方的同键同内容返回原任务及 false
func (s *Service) Submit(ctx context.Context, req Request) (Job, bool, error) {
	if err := s.validate(req); err != nil {
		return Job{}, false, err
	}
	settingsJSON, err := settings(req.Kind)
	if err != nil {
		return Job{}, false, err
	}
	digest := requestDigest(req, settingsJSON)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	var id, oldDigest, oldSettings string
	err = tx.QueryRowContext(ctx, `SELECT id, request_sha256, settings FROM jobs WHERE caller_id = ? AND request_key = ?`, req.CallerID, req.Key).Scan(&id, &oldDigest, &oldSettings)
	if err == nil {
		// 重复请求沿用受理时的设置，避免服务默认值变化使同一请求发生冲突
		if oldDigest != requestDigest(req, oldSettings) {
			return Job{}, false, ErrConflict
		}
		job, err := readJob(ctx, tx, req.CallerID, id)
		return job, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, err
	}
	id, err = newID()
	if err != nil {
		return Job{}, false, err
	}
	now := time.Now().UTC()
	inputHash := sha256Hex(req.Document)
	_, err = tx.ExecContext(ctx, `INSERT INTO jobs (id, caller_id, request_key, request_sha256, kind, title, state, input_sha256, document, settings, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, req.CallerID, req.Key, digest, req.Kind, req.Title, StateQueued, inputHash, req.Document, settingsJSON, now.UnixNano(), now.UnixNano())
	if err != nil {
		return Job{}, false, fmt.Errorf("保存任务: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return Job{}, false, fmt.Errorf("提交任务事务: %w", err)
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return Job{ID: id, Kind: req.Kind, Title: req.Title, Mode: "artifact", State: StateQueued, InputSHA256: inputHash, CreatedAt: now, UpdatedAt: now, Attempts: []Attempt{}}, true, nil
}

// Get 只读取任务元数据，其他调用方的任务与不存在的任务均返回 ErrNotFound
func (s *Service) Get(ctx context.Context, callerID, id string) (Job, error) {
	if !validID(id) {
		return Job{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Job{}, err
	}
	defer tx.Rollback()
	return readJob(ctx, tx, callerID, id)
}

// Artifact 读取调用方已就绪的 PDF 及其 SHA-256；其他状态返回 ErrNotReady
func (s *Service) Artifact(ctx context.Context, callerID, id string) ([]byte, string, error) {
	if !validID(id) {
		return nil, "", ErrNotFound
	}
	var state State
	var artifact []byte
	var digest string
	// 未就绪时不读取文档或产物 BLOB，归属条件与产物读取在同一条查询中生效
	err := s.db.QueryRowContext(ctx, `SELECT state, CASE WHEN state = 'output_ready' THEN artifact END, artifact_sha256 FROM jobs WHERE caller_id = ? AND id = ?`, callerID, id).Scan(&state, &artifact, &digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if state != StateOutputReady {
		return nil, "", ErrNotReady
	}
	return artifact, digest, nil
}

func (s *Service) validate(req Request) error {
	for name, value := range map[string]string{"调用方": req.CallerID, "请求标识": req.Key} {
		if !validText(value, 128) || strings.TrimSpace(value) != value || value == "" {
			return fmt.Errorf("%w: %s须为 1～128 字节且不含控制字符或首尾空白", ErrInvalid, name)
		}
	}
	if !validText(req.Title, 256) {
		return fmt.Errorf("%w: 标题最多 256 字节且不能含控制字符", ErrInvalid)
	}
	if req.Kind != KindHTML && req.Kind != KindPDF {
		return fmt.Errorf("%w: 仅接受 html 或 pdf", ErrInvalid)
	}
	if int64(len(req.Document)) > s.opts.MaxInputBytes {
		return ErrTooLarge
	}
	if len(req.Document) == 0 {
		return fmt.Errorf("%w: 文档为空", ErrInvalid)
	}
	if req.Kind == KindPDF && !bytes.HasPrefix(req.Document, []byte("%PDF-")) {
		return fmt.Errorf("%w: 缺少 PDF 文件头", ErrInvalid)
	}
	return nil
}

func validText(value string, maxBytes int) bool {
	return len(value) <= maxBytes && utf8.ValidString(value) && strings.IndexFunc(value, unicode.IsControl) == -1
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func newID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("生成任务编号: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func settings(kind Kind) (string, error) {
	if kind == KindHTML {
		data, err := json.Marshal(render.DefaultSettings())
		return string(data), err
	}
	return `{}`, nil
}

func requestDigest(req Request, settingsJSON string) string {
	h := sha256.New()
	// 长度前缀避免不同字段组合产生相同的拼接输入
	for _, field := range [][]byte{[]byte(req.Kind), []byte(req.Title), []byte(settingsJSON), req.Document} {
		var size [8]byte
		binary.BigEndian.PutUint64(size[:], uint64(len(field)))
		h.Write(size[:])
		h.Write(field)
	}
	return hex.EncodeToString(h.Sum(nil))
}
