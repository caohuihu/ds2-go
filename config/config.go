package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port                    string
	ProxyAPIKey             string   // primary proxy key: PROXY_API_KEY, or the first accepted key when it is empty
	ProxyKeys               []string // every accepted proxy API key (PROXY_API_KEY + PROXY_KEYS_FILE)
	ProxyKeysFile           string   // one proxy API key per line; default proxy-keys.txt
	BaseURL                 string
	Accounts                []string // DeepSeek web tokens, one per account
	AccountsFile            string
	DefaultModel            string
	ConversationTTLOptions  []time.Duration // per-conversation random TTL pool; empty = never delete
	MaxConvs                int
	AccountQPM              int // per-account requests/min (default 6)
	MaxConcurrentPerAccount int // in-flight concurrency per account (default 5)
	RetryMaxAttempts        int // max 429 retry attempts (default 3)
	RetryBaseDelay          time.Duration
	RequestJitterMin        time.Duration // min random delay before each completion (default 200ms)
	RequestJitterMax        time.Duration // max random delay before each completion (default 800ms)
	PowJitterMin            time.Duration // min artificial delay when solving POW (default 30ms)
	PowJitterMax            time.Duration // max artificial delay when solving POW (default 150ms)
	SessionCreateJitter     time.Duration // random delay before creating a new session (default 200ms)
	DeviceIDRotateHours     int           // rotate deviceID every N hours (0 = disabled, default 0)
	RequestReadTimeMin      time.Duration // min cooldown between requests to same account (simulate reading reply)
	RequestReadTimeMax      time.Duration // max cooldown between requests to same account
	SessionResetChance      float64       // probabilistic session reset chance (0-1)
	RequestsPerSession      int           // max requests per DeepSeek session before forcing a new one (0 = disabled, use SessionResetChance only)
	LogFile                 string        // optional log file path; empty = stdout only
	HealthProbeInterval     time.Duration // how often to actively probe live accounts (0 = disabled, default 30m)
	HealthProbeDeadRetry    time.Duration // how often to re-probe quarantined accounts (0 = disabled, default 10m)
	BanQuarantineHours      int           // how long to hold a BAN-detected account (0 = hold until a probe succeeds, default 168 = 7d)
	DailyRequestLimit       int           // per-account daily request quota (0 = unlimited, default 0)
	LongPauseChance         float64       // chance that a request waits an extra 20–60s human-like pause (0 = off, default 0)
	AutoContinuation        bool          // nudge silent empty upstream replies with a "continue" turn in the same session (default false)
	AutoContinuationMax     int           // max continuation rounds per request (default 2)
	RiskStatsInterval       time.Duration // log a per-account risk summary this often (0 = off, default 0)
	HealthLogFile           string        // dedicated token-health log file; empty = events go to main log only
	StartupAccountCheck     bool          // probe all accounts once before serving (default true)
	ToolJSONFallback        bool          // sniff unmarked bare JSON as tool calls (double-criteria; default false)
	ReasoningFallback       bool          // when the reply body is empty but the thinking block holds the answer, promote it to content (default true)
	ConvFingerprintRoute    bool          // route conversations by their opening fingerprint; first turns start a fresh session on the next round-robin account (default false)
	PersistSessions         bool          // persist the conversation index to a local data file so tasks survive a restart (default false)
	DataDir                 string        // data folder for persisted state; default <LOG_FILE dir>/../data, else ./data
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
		Port:                    env("PORT", "8080"),
		ProxyAPIKey:             env("PROXY_API_KEY", ""),
		ProxyKeysFile:           env("PROXY_KEYS_FILE", "proxy-keys.txt"),
		BaseURL:                 strings.TrimRight(env("DEEPSEEK_BASE_URL", "https://chat.deepseek.com"), "/"),
		AccountsFile:            env("DEEPSEEK_ACCOUNTS_FILE", "accounts.txt"),
		DefaultModel:            env("DEFAULT_MODEL", "deepseek-chat"),
		ConversationTTLOptions:  parseDurationListEnv("CONVERSATION_TTL_OPTIONS", []string{"10m", "60m", "2h", "5h", "0"}),
		MaxConvs:                1024,
		AccountQPM:              parseIntEnv("ACCOUNT_QPM", 6),
		MaxConcurrentPerAccount: parseIntEnv("MAX_CONCURRENT_PER_ACCOUNT", 5),
		RetryMaxAttempts:        parseIntEnv("RETRY_MAX_ATTEMPTS", 3),
		RetryBaseDelay:          d,
		RequestJitterMin:        parseDurationEnv("REQUEST_JITTER_MIN", 200*time.Millisecond),
		RequestJitterMax:        parseDurationEnv("REQUEST_JITTER_MAX", 800*time.Millisecond),
		PowJitterMin:            parseDurationEnv("POW_JITTER_MIN", 30*time.Millisecond),
		PowJitterMax:            parseDurationEnv("POW_JITTER_MAX", 150*time.Millisecond),
		SessionCreateJitter:     parseDurationEnv("SESSION_CREATE_JITTER", 200*time.Millisecond),
		DeviceIDRotateHours:     parseIntEnv("DEVICE_ID_ROTATE_HOURS", 0),
		RequestReadTimeMin:      parseDurationEnv("REQUEST_READ_TIME_MIN", 5*time.Second),
		RequestReadTimeMax:      parseDurationEnv("REQUEST_READ_TIME_MAX", 30*time.Second),
		SessionResetChance:      parseFloatEnv("SESSION_RESET_CHANCE", 0.15),
		RequestsPerSession:      parseIntEnv("REQUESTS_PER_SESSION", 0),
		LogFile:                 env("LOG_FILE", ""),
		HealthProbeInterval:     parseDurationEnv("HEALTH_PROBE_INTERVAL", 30*time.Minute),
		HealthProbeDeadRetry:    parseDurationEnv("HEALTH_PROBE_DEAD_RETRY", 10*time.Minute),
		BanQuarantineHours:      parseIntEnv("BAN_QUARANTINE_HOURS", 168),
		DailyRequestLimit:       parseIntEnv("DAILY_REQUEST_LIMIT", 0),
		LongPauseChance:         parseFloatEnv("LONG_PAUSE_CHANCE", 0),
		AutoContinuation:        parseBoolEnv("AUTO_CONTINUATION", false),
		AutoContinuationMax:     parseIntEnv("AUTO_CONTINUATION_MAX", 2),
		RiskStatsInterval:       parseDurationEnv("RISK_STATS_INTERVAL", 0),
		HealthLogFile:           env("HEALTH_LOG_FILE", ""),
		StartupAccountCheck:     parseBoolEnv("STARTUP_ACCOUNT_CHECK", true),
		ToolJSONFallback:        parseBoolEnv("TOOL_JSON_FALLBACK", false),
		ReasoningFallback:       parseBoolEnv("REASONING_FALLBACK", true),
		ConvFingerprintRoute:    parseBoolEnv("CONV_FINGERPRINT_ROUTE", false),
		PersistSessions:         parseBoolEnv("PERSIST_SESSIONS", false),
	}
	// Proxy API keys: PROXY_API_KEY (optional) plus one-per-line entries in
	// PROXY_KEYS_FILE. When PROXY_API_KEY is empty the file is the source of
	// truth. At least one key is required; the first accepted key is the
	// "primary" one (kept for compatibility / logging). Each key gets a
	// stable id that joins the conversation fingerprints, so distinct keys
	// (distinct clients) can never share a web session.
	{
		fileKeys, err := loadKeysFile(cfg.ProxyKeysFile)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]bool)
		uniq := make([]string, 0, len(fileKeys)+1)
		add := func(k string) {
			if k != "" && !seen[k] {
				seen[k] = true
				uniq = append(uniq, k)
			}
		}
		if cfg.ProxyAPIKey != "" {
			add(cfg.ProxyAPIKey)
		}
		for _, k := range fileKeys {
			add(k)
		}
		if len(uniq) == 0 {
			return nil, fmt.Errorf("no proxy API key configured; set PROXY_API_KEY or %s", cfg.ProxyKeysFile)
		}
		cfg.ProxyKeys = uniq
		if cfg.ProxyAPIKey == "" {
			cfg.ProxyAPIKey = uniq[0]
		}
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

	// Place the token-health log next to the main log file when the user did
	// not choose an explicit location: one folder to tail, in both bare
	// deployments and Docker (where LOG_FILE=/app/logs/app.log is mounted).
	if cfg.HealthLogFile == "" {
		if cfg.LogFile != "" {
			cfg.HealthLogFile = filepath.Join(filepath.Dir(cfg.LogFile), "deepseek-health.log")
		} else {
			cfg.HealthLogFile = "deepseek-health.log"
		}
	}

	// Keep the data folder (persisted session index) one level beside the log
	// folder: LOG_FILE=/app/logs/app.log → /app/data. Bare deployments
	// (stdout logging) default to ./data next to the working directory.
	if d := strings.TrimSpace(os.Getenv("DATA_DIR")); d != "" {
		cfg.DataDir = d
	} else if cfg.LogFile != "" {
		cfg.DataDir = filepath.Join(filepath.Dir(cfg.LogFile), "..", "data")
	} else {
		cfg.DataDir = "data"
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

// loadKeysFile reads proxy API keys, one per line. Blank lines and #
// comments are ignored; an inline " # ..." tail is stripped so
// "my-key  # for SiYuan" yields the key "my-key". A missing file is not an
// error (the env var may carry the key).
func loadKeysFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		for _, sep := range []string{" #", "\t#"} {
			if i := strings.Index(line, sep); i >= 0 {
				line = line[:i]
			}
		}
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
