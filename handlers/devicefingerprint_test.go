package handlers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xgetz/deepseek2api/deepseek"
)

// ── stable per-account device fingerprints (DATA_DIR persistence) ──

func TestApplyFingerprintsStableAcrossRestarts(t *testing.T) {
	tokens := distinctAccountTokens(2)
	path := filepath.Join(t.TempDir(), "device-fingerprints.json")

	// First start: both accounts are new → two records, identities pinned.
	pool1 := NewPool(tokens, 0, 0, 0)
	if n := pool1.ApplyFingerprints(path); n != 2 {
		t.Fatalf("first start: created %d new fingerprints, want 2", n)
	}
	if fp := deepseek.FingerprintOf(pool1.clients[0]); fp.DeviceID == "" || fp.UserAgent == "" || fp.Version == "" || fp.Locale == "" {
		t.Fatalf("first start: fingerprint not fully recorded: %+v", fp)
	}
	if data, err := os.ReadFile(path); err != nil {
		t.Fatalf("fingerprint file not written: %v", err)
	} else if len(data) == 0 {
		t.Fatal("fingerprint file is empty")
	}

	// Simulated restart: a brand-new pool with the SAME tokens must come
	// back with the EXACT same identities (no re-enrolling devices).
	pool2 := NewPool(tokens, 0, 0, 0)
	if n := pool2.ApplyFingerprints(path); n != 0 {
		t.Fatalf("restart: created %d new fingerprints, want 0 (all pinned)", n)
	}
	for i := range pool1.clients {
		want := deepseek.FingerprintOf(pool1.clients[i])
		if got := deepseek.FingerprintOf(pool2.clients[i]); got != want {
			t.Fatalf("restart: account %d identity changed: %v → %v", i, want, got)
		}
	}
}

// accounts.txt grows: the new accounts each enroll once; existing accounts
// keep their records (not re-rolled).
func TestApplyFingerprintsNewAccountsOnlyNewRecords(t *testing.T) {
	tokens := distinctAccountTokens(3)
	path := filepath.Join(t.TempDir(), "device-fingerprints.json")

	pool1 := NewPool(tokens[:1], 0, 0, 0)
	pool1.ApplyFingerprints(path)
	keep0 := deepseek.FingerprintOf(pool1.clients[0])

	// Two new accounts join the pool: exactly 2 new records, account 0
	// untouched.
	pool2 := NewPool(tokens, 0, 0, 0)
	if n := pool2.ApplyFingerprints(path); n != 2 {
		t.Fatalf("with two new accounts: created %d new fingerprints, want 2", n)
	}
	if got := deepseek.FingerprintOf(pool2.clients[0]); got != keep0 {
		t.Fatalf("existing account re-rolled on accounts.txt change: %v → %v", keep0, got)
	}
	if got := deepseek.FingerprintOf(pool2.clients[2]); got.DeviceID == "" {
		t.Fatal("new account got no device id")
	}
}

// A corrupt file must not crash the start: it is quarantined and fresh
// identities are generated instead.
func TestApplyFingerprintsCorruptFileQuarantined(t *testing.T) {
	tokens := distinctAccountTokens(1)
	dir := t.TempDir()
	path := filepath.Join(dir, "device-fingerprints.json")
	if err := os.WriteFile(path, []byte("{not json"), 0644); err != nil {
		t.Fatal(err)
	}

	pool := NewPool(tokens, 0, 0, 0)
	if n := pool.ApplyFingerprints(path); n != 1 {
		t.Fatalf("corrupt file: created %d new fingerprints, want 1 (fresh generation)", n)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var sawLive, sawQuarantined bool
	for _, e := range entries {
		switch {
		case e.Name() == "device-fingerprints.json":
			sawLive = true
		case strings.Contains(e.Name(), ".corrupt-"):
			sawQuarantined = true
		}
	}
	if !sawLive {
		t.Fatal("rewritten fingerprint file is missing")
	}
	if !sawQuarantined {
		t.Fatalf("corrupt original was not quarantined (dir: %v)", dirEntryNames(entries))
	}
}

func dirEntryNames(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// Partial/old records degrade gracefully: only the recorded fields pin.
func TestApplyFingerprintPartialRecord(t *testing.T) {
	c := deepseek.NewClient("acct-partial-0001", 0, 0, 0)
	originalUA := c.UserAgent
	deepseek.ApplyFingerprint(c, deepseek.DeviceFingerprint{DeviceID: "fixed-id", Version: "2.5.0"})
	if c.DeviceID != "fixed-id" || c.Version != "2.5.0" {
		t.Fatalf("recorded fields must pin: %+v", c)
	}
	if c.UserAgent != originalUA {
		t.Fatalf("unrecorded fields must keep the client default, got %q", c.UserAgent)
	}
}
