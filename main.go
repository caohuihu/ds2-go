package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/0xgetz/deepseek2api/config"
	"github.com/0xgetz/deepseek2api/handlers"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	// Set up multi-writer logging: always write to stdout, optionally also to file.
	if cfg.LogFile != "" {
		f, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf("open log file: %v", err)
		}
		log.SetOutput(io.MultiWriter(os.Stdout, f))
		log.Printf("log file enabled: %s", cfg.LogFile)
	}

	convs := handlers.NewConvStore(cfg.ConversationTTLOptions, cfg.MaxConvs, cfg.RequestsPerSession)
	rotateInterval := time.Duration(cfg.DeviceIDRotateHours) * time.Hour
	pool := handlers.NewPool(cfg.Accounts, cfg.PowJitterMin, cfg.PowJitterMax, rotateInterval)
	h := handlers.New(cfg, pool, convs)

	// Token health: dedicated log file + startup check + background probes.
	hc, err := handlers.NewHealthChecker(pool, cfg.HealthLogFile)
	if err != nil {
		log.Printf("health: %v (events will go to the main log only)", err)
	}
	h.SetHealthChecker(hc)
	defer hc.Close()

	if cfg.StartupAccountCheck {
		log.Printf("health: startup account check, probing %d accounts...", len(cfg.Accounts))
		hc.ProbeAll()
		if pool.LiveCount() == 0 {
			log.Printf("health: WARNING: 0/%d accounts usable, requests will fail until tokens are refreshed", len(cfg.Accounts))
		}
	}
	probeCtx, stopProbe := context.WithCancel(context.Background())
	defer stopProbe()
	hc.Start(probeCtx, cfg.HealthProbeInterval, cfg.HealthProbeDeadRetry)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("GET /v1/models", h.Auth(h.ListModels))
	mux.HandleFunc("POST /v1/chat/completions", h.Auth(h.ChatCompletion))

	sweepCtx, stopSweep := context.WithCancel(context.Background())
	defer stopSweep()
	go convs.SweepLoop(sweepCtx)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		log.Printf("deepseek2api listening on :%s", cfg.Port)
		log.Printf("  upstream           : %s", cfg.BaseURL)
		log.Printf("  accounts           : %d", len(cfg.Accounts))
		log.Printf("  default model      : %s", cfg.DefaultModel)
		log.Printf("  conv ttl               : random from %v", cfg.ConversationTTLOptions)
		log.Printf("  account qpm              : %d", cfg.AccountQPM)
		log.Printf("  max concurrent/account   : %d", cfg.MaxConcurrentPerAccount)
		log.Printf("  retry max attempts       : %d (base %s)", cfg.RetryMaxAttempts, cfg.RetryBaseDelay)
		log.Printf("  request jitter           : %s ~ %s", cfg.RequestJitterMin, cfg.RequestJitterMax)
		log.Printf("  pow jitter               : %s ~ %s", cfg.PowJitterMin, cfg.PowJitterMax)
		log.Printf("  session create jitter    : %s", cfg.SessionCreateJitter)
		log.Printf("  device ID rotation       : %s", func() string {
			if cfg.DeviceIDRotateHours > 0 {
				return fmt.Sprintf("every %dh", cfg.DeviceIDRotateHours)
			}
			return "disabled"
		}())
		log.Printf("  request read time        : %s ~ %s", cfg.RequestReadTimeMin, cfg.RequestReadTimeMax)
		log.Printf("  session reset chance     : %.0f%%", cfg.SessionResetChance*100)
		if cfg.HealthLogFile != "" {
			log.Printf("  health log file        : %s", cfg.HealthLogFile)
		}
		log.Printf("  health probe             : live every %s, dead retry every %s (startup check %v)",
			cfg.HealthProbeInterval, cfg.HealthProbeDeadRetry, cfg.StartupAccountCheck)
		if cfg.RequestsPerSession > 0 {
			log.Printf("  requests per session     : %d (auto-rotate after N turns)", cfg.RequestsPerSession)
		}
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("shutting down...")
	stopSweep()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("forced shutdown: %v", err)
	}
}
