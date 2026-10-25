package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

// ── P1: argument-name fuzzy correction ──────────────────────────

func TestFixArgNamesUniqueHit(t *testing.T) {
	tools := []toolDef{{Function: toolFunction{Name: "write", Parameters: json.RawMessage(`{"type":"object","properties":{"filePath":{"type":"string"},"content":{"type":"string"}}}`)}}}
	args := map[string]any{"path": "a.txt", "content": "hi"}
	fixed := fixArgNames(args, toolProperties(findTool(tools, "write")))
	if _, ok := fixed["filePath"]; !ok {
		t.Fatalf("path should be renamed to filePath, got %v", fixed)
	}
	if fixed["content"] != "hi" {
		t.Fatalf("exact key should be preserved: %v", fixed)
	}
}

func TestFixArgNamesAmbiguousKept(t *testing.T) {
	tools := []toolDef{{Function: toolFunction{Name: "write", Parameters: json.RawMessage(`{"type":"object","properties":{"pathName":{"type":"string"},"pathAlias":{"type":"string"}}}`)}}}
	args := map[string]any{"path": "x"}
	fixed := fixArgNames(args, toolProperties(findTool(tools, "write")))
	if _, ok := fixed["path"]; !ok {
		t.Fatalf("ambiguous key must be kept as-is, got %v", fixed)
	}
}

// ── P1: loose JSON repair ───────────────────────────────────────

func TestLooseJSONRepairs(t *testing.T) {
	if v := looseJSON(`{"a":1}`); v == nil {
		t.Fatal("valid JSON must parse")
	}
	if v := looseJSON("```json\n{\"a\":1}\n```"); v == nil {
		t.Fatal("fenced JSON must parse")
	}
	if v := looseJSON(`{"a":1,}`); v == nil {
		t.Fatal("trailing-comma JSON must parse")
	}
	if v := looseJSON(`{"a"：1}`); v == nil {
		t.Fatal("full-width-colon JSON must parse")
	}
	if v := looseJSON("not json at all"); v != nil {
		t.Fatalf("garbage must not parse, got %v", v)
	}
}

// ── P1: bare-JSON fallback (double criterion) ──────────────────

func TestParseToolCallsBareJSONFallback(t *testing.T) {
	// Declared tool + bare JSON that has both name and arguments → accepted
	// only when jsonFallback is enabled.
	text := `Here is the call: {"name": "bash", "arguments": {"command": "ls"}}`
	off, offClean := parseToolCalls(text, testTools, false)
	if len(off) != 0 {
		t.Fatalf("fallback off must not sniff bare JSON: %v", off)
	}
	if offClean != text {
		t.Fatalf("fallback off must leave text untouched")
	}

	on, onClean := parseToolCalls(text, testTools, true)
	if len(on) != 1 || on[0].Name != "bash" {
		t.Fatalf("fallback on must parse bare call, got %v", on)
	}
	if strings.Contains(onClean, `"name"`) {
		t.Fatalf("call JSON must be stripped from clean text: %q", onClean)
	}
}

func TestBareJSONRejectsCodeSamples(t *testing.T) {
	// A {"name":...} object without an arguments/args key is a definition,
	// not a call — the double criterion must reject it.
	text := `The tool is defined as {"name": "bash", "description": "run cmd"}`
	calls, clean := parseToolCalls(text, testTools, true)
	if len(calls) != 0 {
		t.Fatalf("definition-shaped JSON must not become a call")
	}
	if clean != text {
		t.Fatalf("non-call text must be untouched: %q", clean)
	}
}

// ── P1: detector JSON state ─────────────────────────────────────

func TestDetectorJSONState(t *testing.T) {
	block := `Let me run: {"name": "bash", "arguments": {"command": "pwd"}}`
	// With fallback off, the bare JSON must pass through as plain text.
	detOff := newStreamingToolDetector(testTools, false, false)
	var textOff string
	for _, r := range block {
		for _, act := range detOff.feed(string(r)) {
			if act.kind == actionText {
				textOff += act.text
			}
		}
	}
	textOff += textOfActions(detOff.finalize())
	if !strings.Contains(textOff, `"name"`) {
		t.Fatalf("fallback off must keep bare JSON in text: %q", textOff)
	}

	// With fallback on, it must be withheld and emitted as tool_calls.
	detOn := newStreamingToolDetector(testTools, false, true)
	var calls []ToolCall
	var textOn string
	for _, r := range block {
		for _, act := range detOn.feed(string(r)) {
			if act.kind == actionToolCalls {
				calls = append(calls, act.calls...)
			} else {
				textOn += act.text
			}
		}
	}
	for _, act := range detOn.finalize() {
		if act.kind == actionToolCalls {
			calls = append(calls, act.calls...)
		} else {
			textOn += act.text
		}
	}
	if len(calls) != 1 || calls[0].Name != "bash" {
		t.Fatalf("detector JSON state must emit the call: %v", calls)
	}
	if strings.Contains(textOn, `"name"`) {
		t.Fatalf("detector JSON state must withhold bare JSON from text: %q", textOn)
	}
}

func textOfActions(acts []streamAction) string {
	var s string
	for _, a := range acts {
		if a.kind == actionText {
			s += a.text
		}
	}
	return s
}

// ── P2: reasoning fallback ──────────────────────────────────────

func TestShouldPromoteReasoning(t *testing.T) {
	// enabled + empty reply + non-empty thinking + no calls → promote
	if ok, txt := shouldPromoteReasoning("", "the answer", true, false); !ok || txt != "the answer" {
		t.Fatalf("should promote, got (%v,%q)", ok, txt)
	}
	// non-empty reply → no promote
	if ok, _ := shouldPromoteReasoning("body", "thinking", true, false); ok {
		t.Fatal("must not promote when body is present")
	}
	// tool calls → no promote (empty content is normal there)
	if ok, _ := shouldPromoteReasoning("", "thinking", true, true); ok {
		t.Fatal("must not promote when tool calls present")
	}
	// disabled → no promote
	if ok, _ := shouldPromoteReasoning("", "thinking", false, false); ok {
		t.Fatal("must not promote when disabled")
	}
	// blank thinking → no promote
	if ok, _ := shouldPromoteReasoning("", "   ", true, false); ok {
		t.Fatal("must not promote blank thinking")
	}
}

// ── P0: agent-mode gate ────────────────────────────────────────

func TestIsAgentMode(t *testing.T) {
	userMsgs := func() []chatMessage {
		return []chatMessage{{Role: "user", Content: json.RawMessage(`"hi"`)}}
	}
	toolsRaw := json.RawMessage(`[{"type":"function","function":{"name":"bash"}}]`)

	plain := &chatRequest{Messages: userMsgs()}
	if isAgentMode(plain) {
		t.Fatal("plain request must not be agent mode")
	}

	withTools := &chatRequest{Tools: toolsRaw, Messages: userMsgs()}
	if !isAgentMode(withTools) {
		t.Fatal("declared tools must be agent mode")
	}

	// tool_choice=none disables the tools
	noTools := &chatRequest{Tools: toolsRaw, ToolChoice: json.RawMessage(`"none"`), Messages: userMsgs()}
	if isAgentMode(noTools) {
		t.Fatal("tool_choice=none must not be agent mode")
	}

	// a tool result in history makes it agent mode even without declared tools
	toolFlow := &chatRequest{Messages: []chatMessage{
		{Role: "user", Content: json.RawMessage(`"hi"`)},
		{Role: "tool", ToolCallID: "c1", Content: json.RawMessage(`"out"`)},
	}}
	if !isAgentMode(toolFlow) {
		t.Fatal("tool result in history must be agent mode")
	}
}
