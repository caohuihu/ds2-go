package config

import (
	"os"
	"path/filepath"
	"testing"
)

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
