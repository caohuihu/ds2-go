package deepseek

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// DeviceFingerprint is one account's stable client identity as seen by the
// upstream web API: device id, web-client version, locale, user agent and
// timezone offset.
//
// A normal human runs ONE device: the same browser fingerprint for the whole
// life of the account, at most a browser upgrade months later. Re-rolling
// all of these on every process restart makes each account look like a
// device pool constantly re-enrolling — a strong machine tell — so each
// account's fingerprint is generated ONCE and then kept stable via
// LoadFingerprints / SaveFingerprints.
type DeviceFingerprint struct {
	DeviceID  string `json:"device_id"`
	Version   string `json:"version"`
	Locale    string `json:"locale"`
	UserAgent string `json:"user_agent"`
	TZOffset  int    `json:"tz_offset"` // seconds east of UTC; 0 = not recorded
}

// FingerprintOf snapshots the identity fields of a freshly created client
// (NewClient has already picked plausible random values).
func FingerprintOf(c *Client) DeviceFingerprint {
	return DeviceFingerprint{
		DeviceID:  c.DeviceID,
		Version:   c.Version,
		Locale:    c.Locale,
		UserAgent: c.UserAgent,
		TZOffset:  c.TZOffset,
	}
}

// ApplyFingerprint pins the client to a persisted identity. Empty fields
// (and a zero TZOffset) leave the client's current value untouched, so a
// partial or old record degrades gracefully.
func ApplyFingerprint(c *Client, f DeviceFingerprint) {
	if f.DeviceID != "" {
		c.DeviceID = f.DeviceID
	}
	if f.Version != "" {
		c.Version = f.Version
	}
	if f.Locale != "" {
		c.Locale = f.Locale
	}
	if f.UserAgent != "" {
		c.UserAgent = f.UserAgent
	}
	if f.TZOffset != 0 {
		c.TZOffset = f.TZOffset
	}
}

// LoadFingerprints reads a persisted identity map (account-key →
// fingerprint). A missing file returns an empty map; a corrupt file is
// quarantined (renamed aside, logged) instead of crashing — the same
// contract the project's other state files follow.
func LoadFingerprints(path string) (map[string]DeviceFingerprint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]DeviceFingerprint{}, nil
		}
		return nil, err
	}
	out := map[string]DeviceFingerprint{}
	if err := json.Unmarshal(data, &out); err != nil {
		bad := path + ".corrupt-" + time.Now().Format("20060102-150405")
		if rerr := os.Rename(path, bad); rerr != nil {
			log.Printf("fingerprint: %s unreadable (%v), leaving in place", path, err)
		} else {
			log.Printf("fingerprint: %s was corrupt, quarantined as %s (fresh identities)", path, filepath.Base(bad))
		}
		return map[string]DeviceFingerprint{}, fmt.Errorf("deepseek: unreadable fingerprints %s: %w", path, err)
	}
	return out, nil
}

// SaveFingerprints writes the map atomically (tmp + rename), creating the
// parent directory when needed.
func SaveFingerprints(path string, m map[string]DeviceFingerprint) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
