package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ConvPersister snapshots the conversation index (prefix / fingerprint /
// session keys → session records) to a local JSON file so that long-running
// client tasks survive a proxy restart. Writes are throttled (5 s minimum
// interval, coalesced triggers) and atomic (tmp + rename). On startup Load
// restores the in-memory maps; entries whose idle TTL already lapsed or
// whose account no longer exists are dropped — their next request simply
// creates a fresh session on a round-robin account.
type ConvPersister struct {
	path      string
	interval  time.Duration
	mu        sync.Mutex
	lastWrite time.Time
	trigger   chan struct{}
	stop      chan struct{}
	snapshot  func() map[string]ConvRecord
	restore   func(map[string]ConvRecord) (restored, dropped int)
}

type persistFile struct {
	Version int                 `json:"version"`
	SavedAt int64               `json:"saved_at"`
	Entries map[string]ConvRecord `json:"entries"`
}

func NewConvPersister(path string, snapshot func() map[string]ConvRecord, restore func(map[string]ConvRecord) (int, int)) *ConvPersister {
	return &ConvPersister{
		path:     path,
		interval: 5 * time.Second,
		trigger:  make(chan struct{}, 1),
		stop:     make(chan struct{}),
		snapshot: snapshot,
		restore:  restore,
	}
}

// Load reads the persisted file (if any) and restores in-memory state.
// A corrupt or unreadable file is quarantined (<name>.corrupt-<ts>) and an
// empty state is returned instead of crashing.
func (p *ConvPersister) Load() (restored, dropped int) {
	data, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0
		}
		log.Printf("persist: cannot read %s: %v (starting empty)", p.path, err)
		return 0, 0
	}
	var f persistFile
	if err := json.Unmarshal(data, &f); err != nil {
		backup := fmt.Sprintf("%s.corrupt-%d", p.path, time.Now().Unix())
		if renErr := os.Rename(p.path, backup); renErr != nil {
			_ = os.Remove(p.path)
		}
		log.Printf("persist: %s is corrupt (%v); moved to %s, starting empty", p.path, err, backup)
		return 0, 0
	}
	if f.Version != 1 {
		log.Printf("persist: unsupported version %d in %s, starting empty", f.Version, p.path)
		return 0, 0
	}
	restored, dropped = p.restore(f.Entries)
	return restored, dropped
}

// Start launches the background throttled writer.
func (p *ConvPersister) Start() {
	go func() {
		for {
			select {
			case <-p.stop:
				return
			case <-p.trigger:
				p.mu.Lock()
				elapsed := time.Since(p.lastWrite)
				p.mu.Unlock()
				if elapsed < p.interval {
					// Sleep out the throttle window; a second coalesced
					// trigger is drained so the write still happens.
					select {
					case <-time.After(p.interval - elapsed):
						select {
						case <-p.trigger:
						default:
						}
					case <-p.stop:
						return
					}
				}
				p.writeNow()
			}
		}
	}()
}

// Mark requests a throttled write of the current state (non-blocking,
// safe to call from any goroutine, including store-locked paths).
func (p *ConvPersister) Mark() {
	select {
	case p.trigger <- struct{}{}:
	default:
	}
}

// Close stops the background writer and force-flushes the final snapshot.
func (p *ConvPersister) Close() {
	select {
	case <-p.stop:
	default:
		close(p.stop)
	}
	p.writeNow()
}

// writeNow snapshots and atomically rewrites the file.
func (p *ConvPersister) writeNow() {
	entries := p.snapshot()
	if entries == nil {
		entries = map[string]ConvRecord{}
	}
	data, err := json.MarshalIndent(persistFile{Version: 1, SavedAt: time.Now().Unix(), Entries: entries}, "", " ")
	if err != nil {
		log.Printf("persist: marshal failed: %v", err)
		return
	}
	if dir := filepath.Dir(p.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			log.Printf("persist: mkdir %s: %v", dir, err)
			return
		}
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		log.Printf("persist: write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, p.path); err != nil {
		log.Printf("persist: rename %s → %s: %v", tmp, p.path, err)
		return
	}
	p.mu.Lock()
	p.lastWrite = time.Now()
	p.mu.Unlock()
	log.Printf("persist: wrote %d sessions to %s", len(entries), p.path)
}
