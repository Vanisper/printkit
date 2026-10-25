package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"

	"github.com/Vanisper/printkit/internal/jobs"
)

type callerConfig struct {
	ID      string   `json:"id"`
	Token   string   `json:"token"`
	Origins []string `json:"origins"`
}

type config struct {
	Listen                  string         `json:"listen"`
	ChromePath              string         `json:"chrome_path"`
	MaxInputBytes           int64          `json:"max_input_bytes"`
	MaxOutputBytes          int64          `json:"max_output_bytes"`
	ExecutionTimeoutSeconds int            `json:"execution_timeout_seconds"`
	Callers                 []callerConfig `json:"callers"`
}

func defaultDataDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "printkit"), nil
}

func initialize(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := checkDataDir(dir); err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	cfg := config{
		Listen:                  "127.0.0.1:9148",
		MaxInputBytes:           jobs.DefaultMaxInputBytes,
		MaxOutputBytes:          jobs.DefaultMaxOutputBytes,
		ExecutionTimeoutSeconds: int(jobs.DefaultExecutionTimeout.Seconds()),
		Callers: []callerConfig{{ID: "demo", Token: base64.RawURLEncoding.EncodeToString(secret), Origins: []string{
			"http://127.0.0.1:9148", "http://localhost:9148", "http://127.0.0.1:9149",
		}}},
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "config.json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("配置已存在，未覆盖：%s", path)
	}
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(data, '\n'))
	if writeErr == nil {
		writeErr = f.Sync()
	}
	return errors.Join(writeErr, f.Close())
}

func readConfig(dir string) (config, error) {
	var cfg config
	f, err := os.Open(filepath.Join(dir, "config.json"))
	if err != nil {
		return cfg, fmt.Errorf("读取配置（首次使用请先执行 init）：%w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return cfg, err
	}
	if len(data) > 1<<20 {
		return cfg, errors.New("配置不能超过 1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("解析配置：%w", err)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return cfg, errors.New("配置只能包含一个 JSON 对象")
	}
	if err := validateListen(cfg.Listen); err != nil {
		return cfg, err
	}
	if cfg.MaxInputBytes <= 0 || cfg.MaxInputBytes > 100<<20 || cfg.MaxOutputBytes <= 0 || cfg.MaxOutputBytes > 200<<20 {
		return cfg, errors.New("输入和产物上限必须为正整数，分别不能超过 100 MiB 和 200 MiB")
	}
	if cfg.ExecutionTimeoutSeconds < 1 || cfg.ExecutionTimeoutSeconds > 300 {
		return cfg, errors.New("执行超时需在 1–300 秒内")
	}
	if len(cfg.Callers) == 0 {
		return cfg, errors.New("至少配置一个调用方")
	}
	for _, caller := range cfg.Callers {
		if len(caller.Token) < 32 {
			return cfg, errors.New("调用令牌至少需要 32 个字符，请使用 init 生成的随机令牌")
		}
	}
	return cfg, nil
}

func checkDataDir(dir string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("数据目录必须是目录")
	}
	// Windows 依赖用户目录的 ACL，Unix 权限位无法表达其访问控制
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("数据目录允许其他用户访问，请将 %s 的权限设为 0700，或选择新的专用目录", dir)
	}
	return nil
}

func validateListen(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("监听地址格式无效：%w", err)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return errors.New("监听地址必须使用回环 IP，例如 127.0.0.1:9148")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return errors.New("监听端口无效")
	}
	return nil
}
