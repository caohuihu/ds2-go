package handlers

import (
	"encoding/json"
	"strings"
	"testing"
)

var testTools = []toolDef{
	{Function: toolFunction{Name: "get_weather", Description: "Get weather", Parameters: json.RawMessage(`{"properties":{"city":{"type":"string"}},"required":["city"],"type":"object"}`)}},
	{Function: toolFunction{Name: "bash", Description: "Run a command", Parameters: json.RawMessage(`{"properties":{"command":{"type":"string"}},"type":"object"}`)}},
}

func TestParseToolCallsDSMLFull(t *testing.T) {
	text := "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"get_weather\">\n<｜｜DSML｜｜ parameter name=\"city\" string=\"true\">Beijing</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>"
	calls, clean := parseToolCalls(text, testTools)
	if len(calls) != 1 {
		t.Fatalf("want 1 call, got %d", len(calls))
	}
	if calls[0].Name != "get_weather" {
		t.Fatalf("bad name %q", calls[0].Name)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(calls[0].Arguments), &args); err != nil {
		t.Fatalf("bad arguments %q: %v", calls[0].Arguments, err)
	}
	if args["city"] != "Beijing" {
		t.Fatalf("bad city %q", args["city"])
	}
	if strings.TrimSpace(clean) != "" {
		t.Fatalf("clean text not empty: %q", clean)
	}
}

func TestParseToolCallsMultipleInvokes(t *testing.T) {
	text := "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"bash\">\n<｜｜DSML｜｜ parameter name=\"command\" string=\"true\">ls -la</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n<｜｜DSML｜｜ invoke name=\"get_weather\">\n<｜｜DSML｜｜ parameter name=\"city\" string=\"true\">Shanghai</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ calls>"
	calls, _ := parseToolCalls(text, testTools)
	if len(calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(calls))
	}
	if calls[0].Name != "bash" || calls[1].Name != "get_weather" {
		t.Fatalf("bad order: %v", calls)
	}
}

func TestParseToolCallsHalfWidthPipes(t *testing.T) {
	text := "<||DSML|| calls>\n<||DSML|| invoke name=\"bash\">\n<||DSML|| parameter name=\"command\" string=\"true\">echo hi</||DSML|| parameter>\n</||DSML|| invoke>\n</||DSML|| calls>"
	calls, _ := parseToolCalls(text, testTools)
	if len(calls) != 1 || calls[0].Name != "bash" {
		t.Fatalf("half-width pipes not parsed: %v", calls)
	}
	var args map[string]string
	if err := json.Unmarshal([]byte(calls[0].Arguments), &args); err != nil {
		t.Fatalf("bad arguments %q: %v", calls[0].Arguments, err)
	}
	if args["command"] != "echo hi" {
		t.Fatalf("bad command %q", args["command"])
	}
}

func TestParseToolCallsClosedInvokesMissing(t *testing.T) {
	// no </invoke>, no </calls> — implicit split + finalize at end of text
	text := "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"get_weather\">\n<｜｜DSML｜｜ parameter name=\"city\" string=\"true\">Beijing"
	calls, _ := parseToolCalls(text, testTools)
	if len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("unterminated block not recovered: %v", calls)
	}
}

func TestParseToolCallsBareInvoke(t *testing.T) {
	text := "done\n<｜｜DSML｜｜ invoke name=\"bash\">\n<｜｜DSML｜｜ parameter name=\"command\" string=\"true\">pwd</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\nafter"
	calls, clean := parseToolCalls(text, testTools)
	if len(calls) != 1 || calls[0].Name != "bash" {
		t.Fatalf("bare invoke not parsed: %v", calls)
	}
	if !strings.Contains(clean, "done") || !strings.Contains(clean, "after") {
		t.Fatalf("context text lost: %q", clean)
	}
}

func TestParseToolCallsSentinel(t *testing.T) {
	text := "<|tool_calls|>\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"Beijing\"}}\n<|/tool_calls|>"
	calls, clean := parseToolCalls(text, testTools)
	if len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("sentinel not parsed: %v", calls)
	}
	if strings.TrimSpace(clean) != "" {
		t.Fatalf("clean text not empty: %q", clean)
	}
}

func TestParseToolCallsStrictNames(t *testing.T) {
	text := "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"hack_the_planet\">\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>"
	calls, clean := parseToolCalls(text, testTools)
	if len(calls) != 0 {
		t.Fatalf("unknown tool name not dropped: %v", calls)
	}
	// without declared tools the same block passes (lenient)
	calls2, _ := parseToolCalls(text, nil)
	if len(calls2) != 1 {
		t.Fatalf("lenient mode should accept unknown name: %v", calls2)
	}
	_ = clean
}

func TestParseToolCallsNoMarker(t *testing.T) {
	text := "Here is some JSON:\n```json\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"X\"}}\n```\n"
	calls, clean := parseToolCalls(text, testTools)
	if len(calls) != 0 {
		t.Fatalf("marker-less JSON must not become tool calls: %v", calls)
	}
	if clean != text {
		t.Fatalf("text must be untouched")
	}
}

func TestStreamingDetectorByteFeeding(t *testing.T) {
	block := "Sure! <｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"get_weather\">\n<｜｜DSML｜｜ parameter name=\"city\" string=\"true\">Beijing</｜｜DSML｜｜ parameter>\n</｜｜DSML｜｜ invoke>\n</｜｜DSML｜｜ calls>\nDone."
	det := newStreamingToolDetector(testTools, false)
	var texts []string
	var calls []ToolCall
	for _, r := range block { // feed one rune at a time
		for _, act := range det.feed(string(r)) {
			switch act.kind {
			case actionText:
				texts = append(texts, act.text)
			case actionToolCalls:
				calls = append(calls, act.calls...)
			}
		}
	}
	for _, act := range det.finalize() {
		switch act.kind {
		case actionText:
			texts = append(texts, act.text)
		case actionToolCalls:
			calls = append(calls, act.calls...)
		}
	}
	if len(calls) != 1 || calls[0].Name != "get_weather" {
		t.Fatalf("streamed calls not detected: %v", calls)
	}
	out := strings.Join(texts, "")
	if !strings.HasPrefix(out, "Sure! ") {
		t.Fatalf("preamble lost: %q", out)
	}
	if !strings.HasSuffix(out, "Done.") {
		t.Fatalf("trailer lost: %q", out)
	}
	if strings.Contains(out, "DSML") || strings.Contains(out, "invoke") || strings.Contains(out, "parameter") {
		t.Fatalf("DSML artifact leaked into content: %q", out)
	}
}

func TestStreamingDetectorOrphanCloseTag(t *testing.T) {
	// tools mode: orphan closing tags are stripped, never emitted
	det := newStreamingToolDetector(testTools, false)
	acts := det.feed("text </｜｜DSML｜｜ calls> more")
	acts = append(acts, det.finalize()...)
	var text string
	for _, a := range acts {
		if a.kind == actionText {
			text += a.text
		}
	}
	if strings.Contains(text, "DSML") {
		t.Fatalf("orphan close tag leaked: %q", text)
	}
	if text != "text  more" {
		t.Fatalf("unexpected text %q", text)
	}

	// no-tools mode: orphan tags stay ordinary text
	det2 := newStreamingToolDetector(nil, true)
	acts2 := det2.feed("text </｜｜DSML｜｜ calls> more")
	acts2 = append(acts2, det2.finalize()...)
	var text2 string
	for _, a := range acts2 {
		if a.kind == actionText {
			text2 += a.text
		}
	}
	if !strings.Contains(text2, "DSML") {
		t.Fatalf("no-tools mode must keep text intact: %q", text2)
	}
}

func TestStreamingDetectorUnclosedTail(t *testing.T) {
	det := newStreamingToolDetector(testTools, false)
	for _, r := range "<｜｜DSML｜｜ calls>\n<｜｜DSML｜｜ invoke name=\"bash\">\n<｜｜DSML｜｜ parameter name=\"command\" string=\"true\">ls" {
		det.feed(string(r))
	}
	var calls []ToolCall
	for _, act := range det.finalize() {
		calls = append(calls, act.calls...)
	}
	if len(calls) != 1 || calls[0].Name != "bash" {
		t.Fatalf("finalize did not recover unterminated block: %v", calls)
	}
}

func TestStreamingDetectorSentinelFeed(t *testing.T) {
	block := "<|tool_calls|>\n{\"name\": \"bash\", \"arguments\": {\"command\": \"ls\"}}\n<|/tool_calls|>"
	det := newStreamingToolDetector(testTools, false)
	var calls []ToolCall
	for _, r := range block {
		for _, act := range det.feed(string(r)) {
			calls = append(calls, act.calls...)
		}
	}
	for _, act := range det.finalize() {
		calls = append(calls, act.calls...)
	}
	if len(calls) != 1 || calls[0].Name != "bash" {
		t.Fatalf("streamed sentinel not parsed: %v", calls)
	}
}

func TestCanonicalToolCallsStable(t *testing.T) {
	// form A: arguments as a JSON string (the spec form we emit)
	rawA, _ := json.Marshal([]map[string]any{{
		"id": "call_1", "type": "function",
		"function": map[string]any{"name": "bash", "arguments": `{"command":"ls"}`},
	}})
	// form B: arguments re-serialized as an embedded object, different key order
	rawB, _ := json.Marshal([]map[string]any{{
		"function": map[string]any{"arguments": map[string]any{"command": "ls"}, "name": "bash"},
		"type":     "function", "id": "call_1",
	}})
	a := canonicalToolCalls(rawA)
	b := canonicalToolCalls(rawB)
	if a != b {
		t.Fatalf("canonical forms differ:\n%q\n%q", a, b)
	}
}

func TestPrefixKeyIncludesToolCalls(t *testing.T) {
	withCalls := []chatMessage{
		{Role: "user", Content: json.RawMessage(`"hi"`)},
		{Role: "assistant", ToolCalls: json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{}"}}]`)},
		{Role: "tool", ToolCallID: "call_1", Content: json.RawMessage(`"out"`)},
	}
	plain := []chatMessage{
		{Role: "user", Content: json.RawMessage(`"hi"`)},
	}
	if PrefixKey(withCalls, "deepseek-chat") == PrefixKey(plain, "deepseek-chat") {
		t.Fatal("prefix key must distinguish tool flow")
	}
	// re-serialized (different key order) tool_calls hash the same
	reshaped := []chatMessage{
		{Role: "user", Content: json.RawMessage(`"hi"`)},
		{Role: "assistant", ToolCalls: json.RawMessage(`[{"function":{"arguments":{},"name":"bash"},"type":"function","id":"call_1"}]`)},
		{Role: "tool", ToolCallID: "call_1", Content: json.RawMessage(`"out"`)},
	}
	if PrefixKey(withCalls, "deepseek-chat") != PrefixKey(reshaped, "deepseek-chat") {
		t.Fatal("prefix key must be stable across tool_calls re-serialization")
	}
}

func TestRenderToolCallsSentinel(t *testing.T) {
	raw := json.RawMessage(`[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":\"Beijing\"}"}}]`)
	s := renderToolCallsSentinel(raw)
	if !strings.HasPrefix(s, toolOpenPlural) || !strings.HasSuffix(s, toolClosePlural) {
		t.Fatalf("bad sentinel render: %q", s)
	}
	if !strings.Contains(s, `"name":"get_weather"`) {
		t.Fatalf("call not rendered: %q", s)
	}
}

func TestBuildToolPrompt(t *testing.T) {
	p := buildToolPrompt(testTools, toolChoice{kind: "auto"})
	if p == "" {
		t.Fatal("empty prompt")
	}
	for _, want := range []string{"TOOL CALLING PROTOCOL", "get_weather", "bash", "DSML"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lacks %q", want)
		}
	}
	req := buildToolPrompt(testTools, toolChoice{kind: "required"})
	if !strings.Contains(req, "MUST call at least one tool") {
		t.Fatal("required choice instruction absent")
	}
	specific := buildToolPrompt(testTools, toolChoice{kind: "specific", funcName: "bash"})
	if !strings.Contains(specific, "MUST call the tool") {
		t.Fatal("specific choice instruction absent")
	}
	if buildToolPrompt(nil, toolChoice{}) != "" {
		t.Fatal("no tools must yield empty prompt")
	}
}

func TestToolChoiceParsing(t *testing.T) {
	if c := parseToolChoice(json.RawMessage(`"none"`)); c.kind != "none" {
		t.Fatal("none")
	}
	if c := parseToolChoice(json.RawMessage(`{"type":"function","function":{"name":"bash"}}`)); c.kind != "specific" || c.funcName != "bash" {
		t.Fatalf("specific: %+v", c)
	}
	if c := parseToolChoice(nil); c.kind != "auto" {
		t.Fatal("default auto")
	}
	if c := parseToolChoice(json.RawMessage(`"custom_tool"`)); c.kind != "specific" || c.funcName != "custom_tool" {
		t.Fatalf("legacy string choice: %+v", c)
	}
}

func TestParseTools(t *testing.T) {
	tools := parseTools(json.RawMessage(`[{"type":"function","function":{"name":"bash","description":"run"}}]`))
	if len(tools) != 1 || tools[0].Function.Name != "bash" {
		t.Fatalf("bad parse: %+v", tools)
	}
	if parseTools(json.RawMessage(`"not a list"`)) != nil {
		t.Fatal("non-list must yield nil")
	}
}

func TestChatRequestToolFields(t *testing.T) {
	body := `{
		"model": "deepseek-chat",
		"messages": [
			{"role": "system", "content": "you are a bot"},
			{"role": "user", "content": "write a diary"},
			{"role": "assistant", "content": null,
			 "tool_calls": [{"id": "call_1", "type": "function",
				 "function": {"name": "bash", "arguments": "{\"command\":\"date\"}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "2026-10-24"}
		],
		"tools": [{"type": "function", "function": {"name": "bash", "parameters": {"type": "object", "properties": {"command": {"type": "string"}}}}}],
		"tool_choice": "auto",
		"stream": true
	}`
	var req chatRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("bad request json: %v", err)
	}
	if len(req.Messages) != 4 {
		t.Fatalf("msg count: %d", len(req.Messages))
	}
	assistant := req.Messages[2]
	if !strings.Contains(string(assistant.ToolCalls), "bash") {
		t.Fatalf("assistant tool_calls not captured: %s", string(assistant.ToolCalls))
	}
	toolMsg := req.Messages[3]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" {
		t.Fatalf("tool message not captured: %+v", toolMsg)
	}
	if tools := parseTools(req.Tools); len(tools) != 1 || tools[0].Function.Name != "bash" {
		t.Fatalf("tools not parsed: %+v", tools)
	}
	if c := parseToolChoice(req.ToolChoice); c.kind != "auto" {
		t.Fatalf("choice: %+v", c)
	}
	if !hasToolFlow(req.Messages) {
		t.Fatal("hasToolFlow must be true")
	}
	if lastAssistantIndex(req.Messages) != 2 {
		t.Fatalf("last assistant index: %d", lastAssistantIndex(req.Messages))
	}
}
