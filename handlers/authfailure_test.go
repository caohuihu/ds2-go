package handlers

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/0xgetz/deepseek2api/deepseek"
)

// envelopeAuthErr mimics the upstream's HTTP-200 form of an expired/invalid
// userToken: code 40003 "Authorization Failed (invalid token)", wrapped the
// way the completion path wraps it.
var envelopeAuthErr = fmt.Errorf("fetch pow challenge: %w",
	&deepseek.APIError{HTTPStatus: 200, Code: 40003, Msg: "Authorization Failed (invalid token)"})

func TestShouldSelfHealAuthFailure(t *testing.T) {
	// an auth failure is handled by quarantine+migration, never by a
	// same-account session recreate
	if shouldSelfHeal(envelopeAuthErr, false, true, false, false) {
		t.Fatal("envelope 40003 must not self-heal on the same account")
	}
	if shouldSelfHeal(&deepseek.APIError{HTTPStatus: 401}, false, true, false, false) {
		t.Fatal("http 401 must not self-heal")
	}
	// a generic upstream failure of a cached session still self-heals
	if !shouldSelfHeal(&deepseek.APIError{HTTPStatus: 500, Msg: "boom"}, false, true, false, false) {
		t.Fatal("http 500 of a cached session must self-heal")
	}
	// 429 is rate limiting, not a dead session
	if shouldSelfHeal(&deepseek.APIError{HTTPStatus: 429}, false, true, false, false) {
		t.Fatal("429 must not self-heal")
	}
}

func TestMapUpstreamErrorAuthFailure(t *testing.T) {
	status, msg := mapUpstreamError(envelopeAuthErr)
	if status != http.StatusUnauthorized {
		t.Fatalf("envelope 40003 must map to 401, got %d (%q)", status, msg)
	}
	if status, _ = mapUpstreamError(&deepseek.APIError{HTTPStatus: 401}); status != http.StatusUnauthorized {
		t.Fatal("http 401 must map to 401")
	}
	if status, _ = mapUpstreamError(&deepseek.APIError{HTTPStatus: 429}); status != http.StatusTooManyRequests {
		t.Fatal("429 must map to 429")
	}
	if status, _ = mapUpstreamError(&deepseek.APIError{HTTPStatus: 500, Msg: "x"}); status != http.StatusBadGateway {
		t.Fatal("500 must map to 502")
	}
}
