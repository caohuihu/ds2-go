package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProxyKeys(t *testing.T) {
	dir := t.TempDir()
	keysFile := filepath.Join(dir, "proxy-keys.txt")
	os.WriteFile(keysFile, []byte("# 思源笔记\nsiyuan-key-1   # SiYuan\nopencode-key-2\t# opencode\n\n"), 0o644)

	// 1) empty PROXY_API_KEY + keys file → file keys win, primary = first line
	t.Setenv("PROXY_API_KEY", "")
	t.Setenv("PROXY_KEYS_FILE", keysFile)
	t.Setenv("DEEPSEEK_TOKEN", "tok-123")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.ProxyKeys) != 2 || cfg.ProxyKeys[0] != "siyuan-key-1" || cfg.ProxyKeys[1] != "opencode-key-2" {
		t.Fatalf("file keys not parsed (inline comments must be stripped): %v", cfg.ProxyKeys)
	}
	if cfg.ProxyAPIKey != "siyuan-key-1" {
		t.Fatalf("primary key must be the first file key, got %q", cfg.ProxyAPIKey)
	}

	// 2) env key + file keys → merged, env first
	t.Setenv("PROXY_API_KEY", "env-key")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.ProxyKeys) != 3 || cfg.ProxyKeys[0] != "env-key" {
		t.Fatalf("env key must be merged first, got %v", cfg.ProxyKeys)
	}

	// 3) no key anywhere → hard error
	t.Setenv("PROXY_API_KEY", "")
	t.Setenv("PROXY_KEYS_FILE", filepath.Join(dir, "does-not-exist.txt"))
	if _, err = Load(); err == nil {
		t.Fatal("must fail when no proxy key is configured anywhere")
	}
}

func TestHealthLogFileResolvesNextToMainLog(t *testing.T) {
	t.Setenv("PROXY_API_KEY", "test")
	t.Setenv("DEEPSEEK_TOKEN", "tok-123")
	t.Setenv("DEEPSEEK_ACCOUNTS_FILE", filepath.Join(t.TempDir(), "none.txt"))

	// 1) no explicit health log + main log set → same directory as main log
	t.Setenv("LOG_FILE", filepath.Join("app", "logs", "app.log"))
	t.Setenv("HEALTH_LOG_FILE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := filepath.Join("app", "logs", "deepseek-health.log")
	if cfg.HealthLogFile != want {
		t.Fatalf("health log = %q, want %q", cfg.HealthLogFile, want)
	}

	// 2) explicit HEALTH_LOG_FILE always wins
	t.Setenv("HEALTH_LOG_FILE", "custom.log")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HealthLogFile != "custom.log" {
		t.Fatalf("health log = %q, want custom.log", cfg.HealthLogFile)
	}

	// 3) no main log → plain file in the working directory
	_ = os.Unsetenv("LOG_FILE")
	t.Setenv("HEALTH_LOG_FILE", "")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.HealthLogFile != "deepseek-health.log" {
		t.Fatalf("health log = %q, want deepseek-health.log", cfg.HealthLogFile)
	}
}
