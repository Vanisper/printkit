package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gofrs/flock"
)

func TestInitializePreservesCredentials(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Callers[0].Token) < 32 {
		t.Fatal("令牌长度不足")
	}
	if err := initialize(dir); err == nil {
		t.Fatal("重复初始化必须拒绝覆盖")
	}
	again, err := readConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if again.Callers[0].Token != cfg.Callers[0].Token {
		t.Fatal("已有凭据被修改")
	}
	info, err := os.Stat(filepath.Join(dir, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("配置权限过宽：%v", info.Mode().Perm())
	}
	other := filepath.Join(t.TempDir(), "data")
	if err := initialize(other); err != nil {
		t.Fatal(err)
	}
	otherCfg, err := readConfig(other)
	if err != nil {
		t.Fatal(err)
	}
	if otherCfg.Callers[0].Token == cfg.Callers[0].Token {
		t.Fatal("独立初始化复用了令牌")
	}
}

func TestListenIsLoopbackOnly(t *testing.T) {
	for _, address := range []string{"127.0.0.1:9148", "[::1]:0"} {
		if err := validateListen(address); err != nil {
			t.Fatalf("%s: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:9148", ":9148", "192.168.0.1:9148", "localhost:9148", "127.0.0.1:99999"} {
		if err := validateListen(address); err == nil {
			t.Fatalf("意外接受 %s", address)
		}
	}
}

func TestConfigRejectsInvalidValues(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range [][]byte{append(append([]byte{}, original...), []byte("{}")...), []byte(`{"unexpected":1}`), append(append([]byte{}, original...), bytes.Repeat([]byte(" "), 1<<20)...)} {
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readConfig(dir); err == nil {
			t.Fatal("无效配置应被拒绝")
		}
	}
	var cfg config
	if err := json.Unmarshal(original, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg.ExecutionTimeoutSeconds = -1
	body, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfig(dir); err == nil {
		t.Fatal("负数执行超时应被拒绝")
	}
}

func TestPrivateDataDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 使用 ACL")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := initialize(dir); err == nil {
		t.Fatal("不能在共享目录中初始化")
	}
	if err := serve(context.Background(), dir, config{}, &bytes.Buffer{}); err == nil {
		t.Fatal("不能在共享目录中启动")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("拒绝前不应写入凭据")
	}
}

func TestServeRejectsAnotherOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := initialize(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	lock := flock.New(filepath.Join(dir, "instance.lock"))
	locked, err := lock.TryLock()
	if err != nil || !locked {
		t.Fatalf("加锁失败：%v", err)
	}
	defer lock.Close()
	if err := serve(context.Background(), dir, cfg, &bytes.Buffer{}); err == nil {
		t.Fatal("第二个进程不应共享任务目录")
	}
}

func TestCLIHasExplicitCredentialOutput(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	var output, diagnostic bytes.Buffer
	if err := run(context.Background(), []string{"init", "--data-dir", dir}, &output, &diagnostic); err != nil {
		t.Fatal(err)
	}
	cfg, err := readConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(output.Bytes(), []byte(cfg.Callers[0].Token)) {
		t.Fatal("初始化日志不应包含令牌")
	}
	output.Reset()
	if err := run(context.Background(), []string{"token", "--data-dir", dir}, &output, &diagnostic); err != nil {
		t.Fatal(err)
	}
	if output.String() != cfg.Callers[0].Token+"\n" {
		t.Fatal("显式 token 命令未返回对应凭据")
	}
	if err := run(context.Background(), []string{"serve", "unexpected"}, &output, &diagnostic); err == nil {
		t.Fatal("多余参数应报错")
	}
}
