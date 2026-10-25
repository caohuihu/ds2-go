package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port                      string
	ProxyAPIKey               string
	BaseURL                   string
	Accounts                  []string // DeepSeek web tokens, one per account
	AccountsFile              string
	DefaultModel              string
	ConversationTTLOptions    []time.Duration // per-conversation random TTL pool; empty = never delete
	MaxConvs                  int
	AccountQPM                int // per-account requests/min (default 6)
	MaxConcurrentPerAccount   int // in-flight concurrency per account (default 5)
	RetryMaxAttempts          int // max 429 retry attempts (default 3)
	RetryBaseDelay            time.Duration
	RequestJitterMin          time.Duration // min random delay before each completion (default 200ms)
	RequestJitterMax          time.Duration // max random delay before each completion (default 800ms)
	PowJitterMin              time.Duration // min artificial delay when solving POW (default 30ms)
	PowJitterMax              time.Duration // max artificial delay when solving POW (default 150ms)
	SessionCreateJitter       time.Duration // random delay before creating a new session (default 200ms)
	DeviceIDRotateHours       int           // rotate deviceID every N hours (0 = disabled, default 0)
	RequestReadTimeMin        time.Duration // min cooldown between requests to same account (simulate reading reply)
	RequestReadTimeMax        time.Duration // max cooldown between requests to same account
	SessionResetChance        float64       // probabilistic session reset chance (0-1)
	RequestsPerSession        int           // max requests per DeepSeek session before forcing a new one (0 = disabled, use SessionResetChance only)
	LogFile                   string        // optional log file path; empty = stdout only
	HealthProbeInterval       time.Duration // how often to actively probe live accounts (0 = disabled, default 30m)
	HealthProbeDeadRetry      time.Duration // how often to re-probe quarantined accounts (0 = disabled, default 10m)
	HealthLogFile             string        // dedicated token-health log file; empty = events go to main log only
	StartupAccountCheck       bool         // probe all accounts once before serving (default true)
}

// Load reads configuration from the environment. Accounts come from
// DEEPSEEK_TOKEN plus one-per-line entries in DEEPSEEK_ACCOUNTS_FILE.
func Load() (*Config, error) {
	retryBase := env("RETRY_BASE_DELAY", "2s")
	d, err := time.ParseDuration(retryBase)
	if err != nil {
		return nil, fmt.Errorf("invalid RETRY_BASE_DELAY: %w", err)
	}
	cfg := &Config{
		Port:                      env("PORT", "8080"),
		ProxyAPIKey:               env("PROXY_API_KEY", ""),
		BaseURL:                   strings.TrimRight(env("DEEPSEEK_BASE_URL", "https://chat.deepseek.com"), "/"),
		AccountsFile:              env("DEEPSEEK_ACCOUNTS_FILE", "accounts.txt"),
		DefaultModel:              env("DEFAULT_MODEL", "deepseek-chat"),
		ConversationTTLOptions:    parseDurationListEnv("CONVERSATION_TTL_OPTIONS", []string{"10m", "60m", "2h", "5h", "0"}),
		MaxConvs:                  1024,
		AccountQPM:                parseIntEnv("ACCOUNT_QPM", 6),
		MaxConcurrentPerAccount:   parseIntEnv("MAX_CONCURRENT_PER_ACCOUNT", 5),
		RetryMaxAttempts:          parseIntEnv("RETRY_MAX_ATTEMPTS", 3),
		RetryBaseDelay:            d,
		RequestJitterMin:          parseDurationEnv("REQUEST_JITTER_MIN", 200*time.Millisecond),
		RequestJitterMax:          parseDurationEnv("REQUEST_JITTER_MAX", 800*time.Millisecond),
		PowJitterMin:              parseDurationEnv("POW_JITTER_MIN", 30*time.Millisecond),
		PowJitterMax:              parseDurationEnv("POW_JITTER_MAX", 150*time.Millisecond),
		SessionCreateJitter:       parseDurationEnv("SESSION_CREATE_JITTER", 200*time.Millisecond),
		DeviceIDRotateHours:       parseIntEnv("DEVICE_ID_ROTATE_HOURS", 0),
		RequestReadTimeMin:        parseDurationEnv("REQUEST_READ_TIME_MIN", 5*time.Second),
		RequestReadTimeMax:        parseDurationEnv("REQUEST_READ_TIME_MAX", 30*time.Second),
		SessionResetChance:        parseFloatEnv("SESSION_RESET_CHANCE", 0.15),
		RequestsPerSession:        parseIntEnv("REQUESTS_PER_SESSION", 0),
		LogFile:                   env("LOG_FILE", ""),
		HealthProbeInterval:       parseDurationEnv("HEALTH_PROBE_INTERVAL", 30*time.Minute),
		HealthProbeDeadRetry:      parseDurationEnv("HEALTH_PROBE_DEAD_RETRY", 10*time.Minute),
		HealthLogFile:             env("HEALTH_LOG_FILE", "deepseek-health.log"),
		StartupAccountCheck:       parseBoolEnv("STARTUP_ACCOUNT_CHECK", true),
	}
	if cfg.ProxyAPIKey == "" {
		return nil, fmt.Errorf("PROXY_API_KEY is required")
	}
	// Legacy: single CONVERSATION_TTL value still supported (backward compat).
	if v := os.Getenv("CONVERSATION_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			cfg.ConversationTTLOptions = []time.Duration{d}
		}
	}
	if v := os.Getenv("MAX_CONVERSATIONS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid MAX_CONVERSATIONS: %q", v)
		}
		cfg.MaxConvs = n
	}
	if t := strings.TrimSpace(os.Getenv("DEEPSEEK_TOKEN")); t != "" {
		cfg.Accounts = append(cfg.Accounts, t)
	}
	fileAccounts, err := loadAccountsFile(cfg.AccountsFile)
	if err != nil {
		return nil, err
	}
	cfg.Accounts = append(cfg.Accounts, fileAccounts...)
	if len(cfg.Accounts) == 0 {
		return nil, fmt.Errorf("no DeepSeek account configured; set DEEPSEEK_TOKEN or %s", cfg.AccountsFile)
	}
	return cfg, nil
}

func loadAccountsFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func parseIntEnv(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func parseDurationEnv(key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
	}
	return def
}

// parseDurationListEnv parses a comma-separated list of durations from an
// environment variable. "0" in the list is interpreted as time.Duration(0),
// meaning "never expire". If the variable is empty, the provided defaults are
// used.
func parseDurationListEnv(key string, def []string) []time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		out := make([]time.Duration, len(def))
		for i, s := range def {
			out[i], _ = time.ParseDuration(s)
		}
		return out
	}
	var out []time.Duration
	for _, s := range strings.Split(v, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if d, err := time.ParseDuration(s); err == nil {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		out = make([]time.Duration, len(def))
		for i, s := range def {
			out[i], _ = time.ParseDuration(s)
		}
	}
	return out
}

func parseFloatEnv(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f >= 0 && f <= 1 {
			return f
		}
	}
	return def
}

// parseBoolEnv reads a boolean env var ("1", "true", "yes", "on" are true;
// "0", "false", "no", "off" are false), falling back to def when unset or
// unparseable.
func parseBoolEnv(key string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}
