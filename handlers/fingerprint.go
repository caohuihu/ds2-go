package handlers

import (
	"fmt"
	"log"

	"github.com/0xgetz/deepseek2api/deepseek"
)

// ApplyFingerprints pins every account to a stable, persisted device
// fingerprint (device id, web-client version, locale, user agent, timezone
// offset) loaded from path. A real user's device does not re-enroll when the
// proxy restarts, so each account keeps one identity for its whole life
// (keyed by maskToken, surviving accounts.txt reordering). Accounts without
// a record yet keep the plausible random identity NewClient just picked and
// are written to the file for every future start.
//
// Returns the number of NEW records created. Save failures leave the
// identities in-memory only (logged); the proxy keeps working.
//
// Every account's identity is also logged to the main log (LOG_FILE), one
// line per start: `fingerprint: #i/n <mask> [new|pinned] ua=... ver=...
// locale=... tz=+08:00 device=...`, so an operator can grep app.log to see
// exactly which user agent / version / locale / timezone each account is
// presenting. [new] = generated this start and written to the file;
// [pinned] = restored from the file (unchanged since first generation).
func (p *AccountPool) ApplyFingerprints(path string) int {
	fps, err := deepseek.LoadFingerprints(path)
	if err != nil {
		// LoadFingerprints quarantines corrupt files itself.
		fps = map[string]deepseek.DeviceFingerprint{}
	}
	newCount := 0
	n := len(p.clients)
	for i, c := range p.clients {
		key := maskToken(c.Token)
		var tag string
		if f, ok := fps[key]; ok {
			deepseek.ApplyFingerprint(c, f)
			tag = "pinned"
		} else {
			fps[key] = deepseek.FingerprintOf(c)
			newCount++
			tag = "new"
		}
		log.Printf("fingerprint: #%d/%d %s [%s] ua=%s ver=%s locale=%s tz=%s device=%s",
			i+1, n, key, tag, c.UserAgent, c.Version, c.Locale, formatTZ(c.TZOffset), c.DeviceID)
	}
	if err := deepseek.SaveFingerprints(path, fps); err != nil {
		log.Printf("fingerprint: cannot save %s: %v (identities are in-memory only this run)", path, err)
	}
	return newCount
}

// formatTZ renders a seconds-east-of-UTC offset the way the web client
// would display it (+08:00, -05:30).
func formatTZ(offset int) string {
	sign, off := "+", offset
	if off < 0 {
		sign, off = "-", -off
	}
	return fmt.Sprintf("%s%02d:%02d", sign, off/3600, (off%3600)/60)
}
