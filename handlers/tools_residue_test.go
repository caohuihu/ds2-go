package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// runDetector feeds chunks and collects text / calls.
func runDetector(t *testing.T, chunks []string, tools []toolDef, requireMarker, jsonFallback bool) (text string, calls []ToolCall) {
	t.Helper()
	det := newStreamingToolDetector(tools, requireMarker, jsonFallback)
	for _, c := range chunks {
		for _, act := range det.feed(c) {
			if act.kind == actionText {
				text += act.text
			}
			calls = append(calls, act.calls...)
		}
	}
	for _, act := range det.finalize() {
		if act.kind == actionText {
			text += act.text
		}
		calls = append(calls, act.calls...)
	}
	return
}

func argJSON(t *testing.T, c ToolCall, key string) string {
	t.Helper()
	idx := strings.Index(c.Arguments, `"`+key+`"`)
	if idx < 0 {
		t.Fatalf("key %q not found in %q", key, c.Arguments)
	}
	return c.Arguments
}

// ---- degraded forms (leading "<" eaten by the upstream UI) ----

func TestStreamingDegradedCallsBlock(t *testing.T) {
	block := "｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"get_weather\">\n<｜｜DSML｜｜ parameter name=\"city\" string=\"true\">Beijing\n</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>"
	text, calls := runDetector(t, []string{block}, testTools, false, false)
	if len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("want 1 get_weather call, got %d calls %v", len(calls), calls)
	}
	if !strings.Contains(calls[0].Arguments, "Beijing") {
		t.Fatalf("bad args %q", calls[0].Arguments)
	}
	if strings.Contains(text, "DSML") || strings.Contains(text, "invoke") {
		t.Fatalf("residue leaked into text: %q", text)
	}
}

func TestStreamingMalformedInvokeOpen(t *testing.T) {
	// invoke opener written in close shape: </...invoke name="bash">.
	// Must be treated as an opener (it carries name=); a real close
	// "</...invoke>" has no attributes and stays a close.
	block := "｜｜DSML｜｜ calls>\n</｜｜DSML｜｜ invoke name=\"bash\">\n<｜｜DSML｜｜ parameter name=\"command\">ls -la\n</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ calls>"
	text, calls := runDetector(t, []string{block}, testTools, false, false)
	if len(calls) != 1 || calls[0].Name != "bash" {
		t.Fatalf("want 1 bash call, got %d calls %v", len(calls), calls)
	}
	if !strings.Contains(calls[0].Arguments, "ls -la") {
		t.Fatalf("bad args %q", calls[0].Arguments)
	}
	if strings.Contains(text, "DSML") || strings.Contains(text, "invoke") {
		t.Fatalf("residue leaked into text: %q", text)
	}
}

func TestStreamingDegradedOrphanResidue(t *testing.T) {
	// a bare degraded calls fragment with no block behind it is residue:
	// strip it, keep the surrounding prose
	text, calls := runDetector(t, []string{"ok text\n", "｜｜DSML｜｜ calls>", "\nfollow-up prose"}, testTools, false, false)
	if len(calls) != 0 {
		t.Fatalf("want no calls, got %v", calls)
	}
	want := "ok text\n\nfollow-up prose"
	if text != want {
		t.Fatalf("want %q, got %q", want, text)
	}
}

func TestStreamingMarkerTailWithheld(t *testing.T) {
	// an incomplete degraded fragment at the stream tail is withheld,
	// then trimmed at finalize instead of half-emitted
	text, _ := runDetector(t, []string{"a", "｜｜DSML"}, testTools, false, false)
	if text != "a" {
		t.Fatalf("want fragment withheld and trimmed, got %q", text)
	}
	// a completed degraded opener is stripped, following prose kept
	text, calls := runDetector(t, []string{"｜｜DSML｜｜ calls>", "\nend of text"}, testTools, false, false)
	if len(calls) != 0 {
		t.Fatalf("degraded fragment must not become calls, got %v", calls)
	}
	if !strings.Contains(text, "end of text") || strings.Contains(text, "DSML") {
		t.Fatalf("want stripped fragment + kept prose, got %q", text)
	}
}

func TestStreamingRecoverOnCut(t *testing.T) {
	det := newStreamingToolDetector(testTools, false, false)
	// held tail with an unclosed "<" is recovered as plain text
	det.feed("some code <div")
	if got := det.recoverTextOnCut(); got != "<div" {
		t.Fatalf("want %q, got %q", "<div", got)
	}
	// a held marker fragment is trimmed, the rest recovered
	det2 := newStreamingToolDetector(testTools, false, false)
	det2.feed("plain tail ｜｜DSML")
	// the plain part was already emitted during feed; only the fragment
	// is held, so recovery returns nothing
	if got := det2.recoverTextOnCut(); got != "" {
		t.Fatalf("held fragment must stay trimmed, got %q", got)
	}
	// a half tool call is dropped, not recovered
	det3 := newStreamingToolDetector(testTools, false, false)
	det3.feed("<｜｜DSML｜｜ calls>")
	if got := det3.recoverTextOnCut(); got != "" {
		t.Fatalf("DSML-state residue must stay dropped, got %q", got)
	}
}

// ---- tri-state string attribute ----

func TestParseDSMLTriStateStringAttr(t *testing.T) {
	// dedicated tool so fixArgNames keeps every parameter name intact
	tools := []toolDef{{Function: toolFunction{Name: "scratch", Parameters: json.RawMessage(`{"type":"object","properties":{"a":{"type":"string"},"b":{"type":"string"},"c":{"type":"object"},"d":{"type":"string"}}}`)}}}
	// omitted string attr: JSON first, raw fallback
	block := "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"scratch\">\n<｜｜DSML｜｜ parameter name=\"a\">42</｜｜DSML｜｜ parameter>\n<｜｜DSML｜｜ parameter name=\"b\">hello</｜｜DSML｜｜ parameter>\n<｜｜DSML｜｜ parameter name=\"c\" string=\"false\">{\"x\":1}</｜｜DSML｜｜ parameter>\n<｜｜DSML｜｜ parameter name=\"d\" string=\"true\">42</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>"
	calls, _ := parseToolCalls(block, tools, false)
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(calls))
	}
	a := calls[0].Arguments
	if !strings.Contains(a, `"a":42`) {
		t.Fatalf("omitted attr must parse JSON: %s", a)
	}
	if !strings.Contains(a, `"b":"hello"`) {
		t.Fatalf("omitted attr must fall back to string: %s", a)
	}
	if !strings.Contains(a, `"c":{"x":1}`) {
		t.Fatalf("string=false must parse JSON: %s", a)
	}
	if !strings.Contains(a, `"d":"42"`) {
		t.Fatalf("string=true must keep raw: %s", a)
	}
}

// ---- unterminated parameter values ----

func TestParseDSMLParamTruncatedAtNextTag(t *testing.T) {
	// an unterminated parameter value must stop at the next marker tag
	// (incl. degraded residue), so residue can't leak into the value
	block := "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"bash\">\n<｜｜DSML｜｜ parameter name=\"command\">echo hi\n｜｜DSML｜｜ calls> more garbage\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>"
	calls, _ := parseToolCalls(block, testTools, false)
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %d: %v", len(calls), calls)
	}
	aj := argJSON(t, calls[0], "command")
	if !strings.Contains(aj, "echo hi") {
		t.Fatalf("bad command args %q", aj)
	}
	if strings.Contains(aj, "garbage") || strings.Contains(aj, "DSML") {
		t.Fatalf("residue leaked into value: %q", aj)
	}
}

// ---- non-streaming residue scrub ----

func TestParseToolCallsDegradedBlock(t *testing.T) {
	block := "Done.\n｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"get_weather\">\n<｜｜DSML｜｜ parameter name=\"city\" string=\"true\">Beijing\n</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>"
	calls, clean := parseToolCalls(block, testTools, false)
	if len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("want 1 get_weather call, got %d calls %v", len(calls), calls)
	}
	if strings.Contains(clean, "DSML") {
		t.Fatalf("residue leaked: %q", clean)
	}
	if !strings.Contains(clean, "Done.") {
		t.Fatalf("prose lost: %q", clean)
	}
}

func TestParseToolCallsResidueScrubToolsMode(t *testing.T) {
	calls, clean := parseToolCalls("Answer.\n｜｜DSML｜｜ calls> junk", testTools, false)
	if len(calls) != 0 {
		t.Fatalf("want no calls, got %v", calls)
	}
	if strings.Contains(clean, "DSML") {
		t.Fatalf("residue leaked: %q", clean)
	}
	if !strings.Contains(clean, "Answer.") {
		t.Fatalf("prose lost: %q", clean)
	}
}

func TestParseToolCallsNoToolsKeepsText(t *testing.T) {
	// without tools the marker protocol was never taught: fragments stay
	// ordinary text
	_, clean := parseToolCalls("text</｜｜DSML｜｜ calls> more", nil, false)
	if clean != "text</｜｜DSML｜｜ calls> more" {
		t.Fatalf("no-tools mode must keep text intact: %q", clean)
	}
}

func TestStreamingNoToolsKeepsDegradedText(t *testing.T) {
	text, _ := runDetector(t, []string{"text </｜｜DSML｜｜ calls> more"}, nil, true, false)
	if text != "text </｜｜DSML｜｜ calls> more" {
		t.Fatalf("no-tools mode must keep text intact: %q", text)
	}
}
