package handlers

import (
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
func (p *AccountPool) ApplyFingerprints(path string) int {
	fps, err := deepseek.LoadFingerprints(path)
	if err != nil {
		// LoadFingerprints quarantines corrupt files itself.
		fps = map[string]deepseek.DeviceFingerprint{}
	}
	newCount := 0
	for _, c := range p.clients {
		key := maskToken(c.Token)
		if f, ok := fps[key]; ok {
			deepseek.ApplyFingerprint(c, f)
			continue
		}
		fps[key] = deepseek.FingerprintOf(c)
		newCount++
	}
	if err := deepseek.SaveFingerprints(path, fps); err != nil {
		log.Printf("fingerprint: cannot save %s: %v (identities are in-memory only this run)", path, err)
	}
	return newCount
}
