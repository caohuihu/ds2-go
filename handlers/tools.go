package handlers

import (
	"encoding/json"
	"log"
	"regexp"
	"strings"
	"sync"
)

// OpenAI-compatible tool calling for the DeepSeek web backend.
//
// The web backend is a plain text stream with no native function calling.
// This file simulates it at the text layer (same approach as webai2api):
//
//  1. buildToolPrompt teaches the model the DSML call format
//     (<｜｜DSML｜｜ calls> ...), DeepSeek's native tool prior.
//  2. parseToolCalls / streamingToolDetector extract the calls from the
//     model output and strip every marker fragment from the content so no
//     artifact ever reaches the client ("zero artifact tolerance").
//  3. renderToolCallsSentinel turns echoed assistant tool_calls back into
//     text the model understands, and canonicalToolCalls makes the
//     conversation-prefix hash stable across client re-serialization.
//
// Accepted marker forms (tolerance matrix, ported from webai2api):
//   - pipes: | (U+007C) and ｜ (U+FF5C), single/double/mixed
//   - DSML / invoke / parameter case-insensitive, optional inner spaces
//   - attribute values: "x" / 'x' / bare, spaces around =
//   - missing </invoke>: implicit split at the next <invoke
//   - missing </calls>: finalized at stream end
//   - orphan closing tags: silently stripped, never leaked into content

// ToolCall is one parsed function call in OpenAI wire form.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON string
}

func (c ToolCall) toOpenAI() map[string]any {
	return map[string]any{
		"id":   c.ID,
		"type": "function",
		"function": map[string]any{
			"name":      c.Name,
			"arguments": c.Arguments,
		},
	}
}

// ---- request-side types ----

type toolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type toolDef struct {
	Type     string       `json:"type"`
	Function toolFunction `json:"function"`
}

func parseTools(raw json.RawMessage) []toolDef {
	if len(raw) == 0 {
		return nil
	}
	var defs []toolDef
	if json.Unmarshal(raw, &defs) != nil {
		return nil
	}
	out := make([]toolDef, 0, len(defs))
	for _, d := range defs {
		if d.Function.Name != "" {
			out = append(out, d)
		}
	}
	return out
}

// toolChoice mirrors OpenAI's tool_choice.
type toolChoice struct {
	kind     string // "auto", "none", "required", or "specific"
	funcName string // when kind == "specific"
}

func parseToolChoice(raw json.RawMessage) toolChoice {
	if len(raw) == 0 {
		return toolChoice{kind: "auto"}
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case "none", "auto", "required", "valid":
			return toolChoice{kind: s}
		}
		return toolChoice{kind: "specific", funcName: s}
	}
	var o struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &o) == nil && o.Function.Name != "" {
		return toolChoice{kind: "specific", funcName: o.Function.Name}
	}
	return toolChoice{kind: "auto"}
}

// isJSONMode reports whether response_format requests json_object.
func isJSONMode(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var rf struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &rf) == nil && rf.Type == "json_object"
}

// hasToolFlow reports whether the message history already contains an
// agent tool loop (tool results or assistant tool_calls).
func hasToolFlow(msgs []chatMessage) bool {
	for _, m := range msgs {
		if m.Role == "tool" || (m.Role == "assistant" && len(m.ToolCalls) > 0) {
			return true
		}
	}
	return false
}

// lastAssistantIndex returns the index of the last assistant message, or -1.
func lastAssistantIndex(msgs []chatMessage) int {
	idx := -1
	for i := range msgs {
		if msgs[i].Role == "assistant" {
			idx = i
		}
	}
	return idx
}

// ---- sentinel constants ----

const (
	toolOpenSingular  = "<|tool_call|>"
	toolOpenPlural    = "<|tool_calls|>"
	toolCloseSingular = "<|/tool_call|>"
	toolClosePlural   = "<|/tool_calls|>"
)

var openVariants = []string{toolOpenPlural, toolOpenSingular}
var closeVariants = []string{toolClosePlural, toolCloseSingular}

// ---- DSML regexes (tolerate every common variant) ----

// dsmlMarkRE is pipe(s)+DSML+pipe(s): half- or full-width, mixed, spaced,
// case-insensitive. The prompt keeps the full-width form (native prior).
var dsmlMarkRE = `[|\x{FF5C}]+\s*DSML\s*[|\x{FF5C}]+`

// tagAttrsRE allows name="x" / name='x' / name=x; values may contain '>'.
const tagAttrsRE = `(?:"[^"]*"|'[^']*'|[^>"'])*`

var (
	dsmlCallsOpenRE   = regexp.MustCompile(`(?i)<\s*` + dsmlMarkRE + `\s*calls\b` + tagAttrsRE + `>`)
	dsmlCallsCloseRE  = regexp.MustCompile(`(?i)</\s*` + dsmlMarkRE + `\s*calls\s*>`)
	dsmlInvokeOpenRE  = regexp.MustCompile(`(?i)<\s*` + dsmlMarkRE + `\s*invoke\b(` + tagAttrsRE + `)>`)
	dsmlInvokeCloseRE = regexp.MustCompile(`(?i)</\s*` + dsmlMarkRE + `\s*invoke\s*>`)
	dsmlParamOpenRE   = regexp.MustCompile(`(?i)<\s*` + dsmlMarkRE + `\s*parameter\b(` + tagAttrsRE + `)>`)
	dsmlParamCloseRE  = regexp.MustCompile(`(?i)</\s*` + dsmlMarkRE + `\s*parameter\s*>`)
	dsmlAttrPairRE    = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
	dsmlAnyOpenRE     = regexp.MustCompile(`(?i)<\s*` + dsmlMarkRE + `\s*(?:calls|invoke)\b`)
	dsmlNoiseRE       = regexp.MustCompile(`(?i)</\s*` + dsmlMarkRE + `\s*(?:calls|invoke|parameter)\s*>`)
)

// dsmlFull is the marker used inside prompts (full-width native prior).
const dsmlFull = "｜｜DSML｜｜"

// safeKeep bounds the SCAN tail that may be the start of a marker.
const safeKeep = 64

// ---- tool prompt ----

var toolPromptCache sync.Map // string -> string

// buildToolPrompt renders the DSML tool-calling protocol plus the tool
// schema. Cached per (tools, choice) since agent loops resend the same
// tools every turn.
func buildToolPrompt(tools []toolDef, choice toolChoice) string {
	if len(tools) == 0 {
		return ""
	}
	key := string(mustMarshal(tools)) + "\x00" + choice.kind + "\x00" + choice.funcName
	if v, ok := toolPromptCache.Load(key); ok {
		return v.(string)
	}
	p := buildToolPromptImpl(tools, choice)
	toolPromptCache.Store(key, p)
	return p
}

func buildToolPromptImpl(tools []toolDef, choice toolChoice) string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Function.Name)
	}

	var b strings.Builder
	b.WriteString("\n==== TOOL CALLING PROTOCOL ====\n")
	b.WriteString("\n!!! CRITICAL FORMAT RULE !!!\n")
	b.WriteString("When you need a tool, the FIRST characters of your response\n")
	b.WriteString("MUST be the literal token <" + dsmlFull + " calls>.")
	b.WriteString("If you write any sentence before it, the call is INVALID and\n")
	b.WriteString("the system WILL NOT execute the tool.\n")
	b.WriteString("\nWRONG (the tool will NOT run):\n")
	b.WriteString("  Sure, I'll check the weather for you.\n")
	b.WriteString("  {\"name\": \"get_weather\", \"arguments\": {\"city\": \"Beijing\"}}\n")
	b.WriteString("\nRIGHT:\n")
	b.WriteString("  <" + dsmlFull + " calls>\n")
	b.WriteString("  <" + dsmlFull + ` invoke name="get_weather">` + "\n")
	b.WriteString("  <" + dsmlFull + ` parameter name="city" string="true">Beijing</` + dsmlFull + " parameter>\n")
	b.WriteString("  </" + dsmlFull + " invoke>\n")
	b.WriteString("  </" + dsmlFull + " calls>\n")
	b.WriteString("\nMulti-tool example (multiple invokes INSIDE ONE calls block):\n")
	b.WriteString("  <" + dsmlFull + " calls>\n")
	b.WriteString("  <" + dsmlFull + ` invoke name="read">` + "\n")
	b.WriteString("  <" + dsmlFull + ` parameter name="file_path" string="true">/etc/hosts</` + dsmlFull + " parameter>\n")
	b.WriteString("  </" + dsmlFull + " invoke>\n")
	b.WriteString("  <" + dsmlFull + ` invoke name="bash">` + "\n")
	b.WriteString("  <" + dsmlFull + ` parameter name="command" string="true">ls -la</` + dsmlFull + " parameter>\n")
	b.WriteString("  </" + dsmlFull + " invoke>\n")
	b.WriteString("  </" + dsmlFull + " calls>\n")
	b.WriteString("\nRules:\n")
	b.WriteString("  - No text, no markdown, no code fences outside the <" + dsmlFull + " calls> block.\n")
	b.WriteString("  - ONE <" + dsmlFull + ` calls> block per response; wrap EACH tool call in its own <` + dsmlFull + ` invoke name="...">.` + "\n")
	b.WriteString("  - Every argument uses <" + dsmlFull + ` parameter name="..." string="true|false">value</` + dsmlFull + " parameter>.\n")
	b.WriteString("  - Use string=\"true\" for text values; string=\"false\" (or omit) for numbers/objects/arrays.\n")
	b.WriteString("  - Do NOT emit " + toolOpenPlural + " or " + toolOpenSingular + " sentinel tokens.\n")
	b.WriteString("  - Do NOT emit a trailing <" + dsmlFull + " calls> block with no invokes.\n")
	b.WriteString("  - Use EXACT tool names: " + strings.Join(names, ", ") + "\n")
	b.WriteString("\n## Tools\n")
	b.WriteString("\n")

	for _, t := range tools {
		b.WriteString("### " + t.Function.Name + "\n")
		if t.Function.Description != "" {
			b.WriteString(t.Function.Description + "\n")
		}
		if len(t.Function.Parameters) > 0 {
			var params struct {
				Properties map[string]struct {
					Type        string `json:"type"`
					Description string `json:"description"`
				} `json:"properties"`
				Required []string `json:"required"`
			}
			if json.Unmarshal(t.Function.Parameters, &params) == nil {
				reqSet := make(map[string]bool, len(params.Required))
				for _, r := range params.Required {
					reqSet[r] = true
				}
				b.WriteString("Parameters:\n")
				for name, p := range params.Properties {
					ptype := p.Type
					if ptype == "" {
						ptype = "string"
					}
					line := "  - " + name + " (" + ptype
					if reqSet[name] {
						line += ", required"
					}
					line += "): " + p.Description
					b.WriteString(line + "\n")
				}
				b.WriteString("\n")
			}
		}
	}

	switch choice.kind {
	case "required":
		b.WriteString(">> You MUST call at least one tool in every response.\n")
	case "specific":
		b.WriteString(">> You MUST call the tool `" + choice.funcName + "`.\n")
	}
	return b.String()
}

// ---- name matching / normalization ----

func normToolName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	return strings.ReplaceAll(n, "-", "_")
}

func matchToolName(name string, known map[string]bool) bool {
	if len(known) == 0 {
		return true
	}
	if known[name] {
		return true
	}
	n := normToolName(name)
	for k := range known {
		if normToolName(k) == n {
			return true
		}
	}
	return false
}

func knownToolNames(tools []toolDef) map[string]bool {
	known := map[string]bool{}
	for _, t := range tools {
		known[t.Function.Name] = true
	}
	return known
}

// findTool locates the declared tool for a (fuzzy) name.
func findTool(tools []toolDef, name string) *toolDef {
	if !matchToolName(name, knownToolNames(tools)) {
		return nil
	}
	for i := range tools {
		if tools[i].Function.Name == name || normToolName(tools[i].Function.Name) == normToolName(name) {
			return &tools[i]
		}
	}
	return nil
}

// toolProperties extracts the JSON-schema property keys of one tool.
func toolProperties(t *toolDef) map[string]any {
	if t == nil || len(t.Function.Parameters) == 0 {
		return nil
	}
	var p struct {
		Properties map[string]any `json:"properties"`
	}
	if json.Unmarshal(t.Function.Parameters, &p) == nil {
		return p.Properties
	}
	return nil
}

// fixArgNames corrects argument key names to the schema's parameter names.
// Small models often deviate (path vs filePath): a wrong key is renamed only
// when a case-insensitive substring match against the schema properties is
// unique; ambiguous or unknown keys are kept as-is.
func fixArgNames(args map[string]any, properties map[string]any) map[string]any {
	if len(args) == 0 || len(properties) == 0 {
		return args
	}
	lower := make(map[string]string, len(properties))
	for k := range properties {
		lower[strings.ToLower(k)] = k
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		if _, ok := properties[k]; ok {
			out[k] = v
			continue
		}
		missed := strings.ToLower(k)
		var hits []string
		for lk, orig := range lower {
			if strings.Contains(lk, missed) {
				hits = append(hits, orig)
			}
		}
		if len(hits) == 1 {
			out[hits[0]] = v
		} else {
			out[k] = v
		}
	}
	return out
}

// normalizeToolCall validates a call against the declared tool set. With a
// non-empty tool set, unknown names are dropped (strict mode) to keep model
// hallucinations out of the agent loop. Argument key names are corrected
// against the schema when the model deviates (see fixArgNames).
func normalizeToolCall(id, name string, args any, tools []toolDef) *ToolCall {
	if name == "" {
		return nil
	}
	if len(tools) > 0 && !matchToolName(name, knownToolNames(tools)) {
		log.Printf("tools: dropping unknown tool name %q", name)
		return nil
	}
	if m, ok := args.(map[string]any); ok {
		args = fixArgNames(m, toolProperties(findTool(tools, name)))
	}
	call := ToolCall{ID: id, Name: name, Arguments: coerceArguments(args)}
	if call.ID == "" {
		call.ID = "call_" + randHex(24)
	}
	return &call
}

func coerceArguments(args any) string {
	switch v := args.(type) {
	case nil:
		return "{}"
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return "{}"
		}
		if looseJSON(s) != nil {
			return s
		}
		return mustMarshalString(map[string]any{"value": v})
	default:
		return mustMarshalString(v)
	}
}

// trailingCommaRE strips trailing commas that models add before } / ].
var trailingCommaRE = regexp.MustCompile(`,\s*([}\]])`)

// fixFullWidth turns full-width punctuation models leak into JSON into ASCII.
func fixFullWidth(s string) string {
	s = strings.ReplaceAll(s, "，", ",")
	s = strings.ReplaceAll(s, "：", ":")
	return s
}

// looseJSON is a tolerant JSON parse: code fences, a leading "json" fence
// label, trailing commas and full-width punctuation are repaired before
// giving up. Returns nil when the input is not (repairable) JSON.
func looseJSON(raw string) any {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, "`")
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "json") {
		raw = strings.TrimSpace(raw[len("json"):])
	}
	if raw == "" {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(raw), &v) == nil {
		return v
	}
	s, e := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if s < 0 || e <= s {
		return nil
	}
	frag := raw[s : e+1]
	candidates := []string{
		frag,
		trailingCommaRE.ReplaceAllString(frag, "$1"),
		fixFullWidth(frag),
		trailingCommaRE.ReplaceAllString(fixFullWidth(frag), "$1"),
	}
	for _, c := range candidates {
		if json.Unmarshal([]byte(c), &v) == nil {
			return v
		}
	}
	return nil
}

func mustMarshalString(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ---- DSML body parsing (recovers missing close tags) ----

func parseDSMLAttrs(s string) map[string]string {
	res := map[string]string{}
	if s == "" {
		return res
	}
	for _, m := range dsmlAttrPairRE.FindAllSubmatch([]byte(s), -1) {
		key := string(m[1])
		val := string(m[2])
		if val == "" {
			val = string(m[3])
		}
		if val == "" {
			val = string(m[4])
		}
		res[key] = val
	}
	return res
}

func decodeDSMLValue(raw string, isString bool) any {
	if isString {
		return raw
	}
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if v := looseJSON(s); v != nil {
		return v
	}
	return raw
}

// parseDSMLBody parses the invoke blocks inside a DSML calls body.
func parseDSMLBody(body string, tools []toolDef) []ToolCall {
	var calls []ToolCall
	if body == "" {
		return calls
	}
	pos := 0
	for {
		idx := dsmlInvokeOpenRE.FindSubmatchIndex([]byte(body[pos:]))
		if idx == nil {
			break
		}
		openStart, openEnd := pos+idx[0], pos+idx[1]
		attrsStr := ""
		if idx[2] >= 0 {
			attrsStr = body[pos+idx[2] : pos+idx[3]]
		}
		attrs := parseDSMLAttrs(attrsStr)
		name := attrs["name"]

		var inner string
		var nextPos int
		if cidx := dsmlInvokeCloseRE.FindStringIndex(body[openEnd:]); cidx != nil {
			inner = body[openEnd : openEnd+cidx[0]]
			nextPos = openEnd + cidx[1]
		} else {
			// missing </invoke>: implicit split at the next <invoke
			if nidx := dsmlInvokeOpenRE.FindStringIndex(body[openEnd:]); nidx != nil {
				inner = body[openEnd : openEnd+nidx[0]]
				nextPos = openEnd + nidx[0]
			} else {
				inner = body[openEnd:]
				nextPos = len(body)
			}
		}
		_ = openStart

		arguments := map[string]any{}
		ppos := 0
		for {
			pidx := dsmlParamOpenRE.FindSubmatchIndex([]byte(inner[ppos:]))
			if pidx == nil {
				break
			}
			pEnd := ppos + pidx[1]
			pAttrsStr := ""
			if pidx[2] >= 0 {
				pAttrsStr = inner[ppos+pidx[2] : ppos+pidx[3]]
			}
			pAttrs := parseDSMLAttrs(pAttrsStr)
			pName := pAttrs["name"]
			sv := pAttrs["string"]
			isString := sv == "" || sv == "true" || sv == "1" || sv == "yes"

			var rawValue string
			var pnext int
			if pcidx := dsmlParamCloseRE.FindStringIndex(inner[pEnd:]); pcidx != nil {
				rawValue = inner[pEnd : pEnd+pcidx[0]]
				pnext = pEnd + pcidx[1]
			} else {
				// unterminated parameter: consume to the end of the invoke
				rawValue = inner[pEnd:]
				pnext = len(inner)
			}
			if pName != "" {
				arguments[pName] = decodeDSMLValue(rawValue, isString)
			}
			if pnext <= ppos {
				pnext = ppos + 1
			}
			ppos = pnext
		}

		if call := normalizeToolCall("", name, arguments, tools); call != nil {
			calls = append(calls, *call)
		}
		if nextPos <= pos {
			nextPos = pos + 1
		}
		pos = nextPos
	}
	return calls
}

// ---- sentinel body parsing ----

// parseSentinelBody parses one JSON object per line: {"name":..., "arguments":...}.
func parseSentinelBody(body string, tools []toolDef) []ToolCall {
	var calls []ToolCall
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		data := looseJSON(line)
		m, ok := data.(map[string]any)
		if !ok {
			continue
		}
		if call := normalizeSentinelData(m, tools); call != nil {
			calls = append(calls, *call)
		}
	}
	return calls
}

// normalizeSentinelData turns one {"id","name"/"function","arguments"} object
// into a validated ToolCall (loose JSON in, spec JSON string out).
func normalizeSentinelData(data map[string]any, tools []toolDef) *ToolCall {
	id, _ := data["id"].(string)
	name, _ := data["name"].(string)
	if name == "" {
		if fn, ok := data["function"].(map[string]any); ok {
			name, _ = fn["name"].(string)
		}
	}
	args := data["arguments"]
	if args == nil {
		if fn, ok := data["function"].(map[string]any); ok {
			args = fn["arguments"]
		}
	}
	return normalizeToolCall(id, name, args, tools)
}

// parseJSONCalls accepts the JSON shapes a call blob may take: a
// {"tool_calls":[...]} wrapper, a single call object, or a bare list.
func parseJSONCalls(raw string, tools []toolDef) []ToolCall {
	data := looseJSON(raw)
	if data == nil {
		return nil
	}
	switch v := data.(type) {
	case map[string]any:
		if tcs, ok := v["tool_calls"].([]any); ok {
			var calls []ToolCall
			for _, x := range tcs {
				if m, ok := x.(map[string]any); ok {
					if c := normalizeSentinelData(m, tools); c != nil {
						calls = append(calls, *c)
					}
				}
			}
			return calls
		}
		if c := normalizeSentinelData(v, tools); c != nil {
			return []ToolCall{*c}
		}
	case []any:
		var calls []ToolCall
		for _, x := range v {
			if m, ok := x.(map[string]any); ok {
				if c := normalizeSentinelData(m, tools); c != nil {
					calls = append(calls, *c)
				}
			}
		}
		return calls
	}
	return nil
}

// ---- whole-text parsing (non-streaming) ----

// tryDSML locates and parses a DSML block in its three tolerated shapes.
func tryDSML(text string, tools []toolDef) ([]ToolCall, string) {
	// Shape 1: complete <calls> ... </calls>
	open := dsmlCallsOpenRE.FindStringIndex(text)
	if open != nil {
		afterOpen := open[1]
		if cidx := dsmlCallsCloseRE.FindStringIndex(text[afterOpen:]); cidx != nil {
			body := text[afterOpen : afterOpen+cidx[0]]
			if calls := parseDSMLBody(body, tools); len(calls) > 0 {
				clean := strings.TrimSpace(text[:open[0]] + text[afterOpen+cidx[1]:])
				return calls, clean
			}
		} else {
			// opening tag without a calls close: parse to the end
			body := text[afterOpen:]
			if calls := parseDSMLBody(body, tools); len(calls) > 0 {
				lastClose := -1
				for _, m := range dsmlInvokeCloseRE.FindAllStringIndex(text[afterOpen:], -1) {
					lastClose = afterOpen + m[1]
				}
				var clean string
				if lastClose > 0 {
					clean = strings.TrimSpace(text[:open[0]] + text[lastClose:])
				} else {
					clean = strings.TrimSpace(text[:open[0]])
				}
				return calls, clean
			}
		}
	}

	// Shape 2: calls close without a calls open
	if cidx := dsmlCallsCloseRE.FindStringIndex(text); cidx != nil {
		inv := dsmlInvokeOpenRE.FindStringIndex(text[:cidx[0]])
		if inv != nil {
			body := text[inv[0]:cidx[0]]
			if calls := parseDSMLBody(body, tools); len(calls) > 0 {
				clean := strings.TrimSpace(text[:inv[0]] + text[cidx[1]:])
				return calls, clean
			}
		}
	}

	// Shape 3: bare invoke(s)
	if inv := dsmlInvokeOpenRE.FindStringIndex(text); inv != nil {
		body := text[inv[0]:]
		if calls := parseDSMLBody(body, tools); len(calls) > 0 {
			lastClose := -1
			for _, m := range dsmlInvokeCloseRE.FindAllStringIndex(text[inv[0]:], -1) {
				lastClose = inv[0] + m[1]
			}
			var clean string
			if lastClose > 0 {
				clean = strings.TrimSpace(text[:inv[0]] + text[lastClose:])
			} else {
				clean = strings.TrimSpace(text[:inv[0]])
			}
			return calls, clean
		}
	}

	return nil, ""
}

// trySentinel locates a <|tool_calls|> block with one JSON call per line.
func trySentinel(text string, tools []toolDef) ([]ToolCall, string) {
	for _, ov := range openVariants {
		oIdx := strings.Index(text, ov)
		if oIdx < 0 {
			continue
		}
		cIdx, cv := -1, ""
		for _, cvv := range closeVariants {
			if i := strings.Index(text[oIdx+len(ov):], cvv); i >= 0 {
				cIdx = oIdx + len(ov) + i
				cv = cvv
				break
			}
		}
		if cIdx < 0 {
			continue
		}
		body := text[oIdx+len(ov) : cIdx]
		if calls := parseSentinelBody(body, tools); len(calls) > 0 {
			clean := strings.TrimSpace(text[:oIdx] + text[cIdx+len(cv):])
			return calls, clean
		}
	}
	return nil, ""
}

// parseToolCalls extracts tool calls from a completed reply. Priority:
// DSML, then sentinel. Marker-less bare JSON is only sniffed when
// jsonFallback is enabled (opt-in), with a double criterion so ordinary
// JSON in answers and code samples stays text: the name key must hit the
// declared whitelist AND the object must carry an arguments/args key.
func parseToolCalls(text string, tools []toolDef, jsonFallback bool) (calls []ToolCall, cleanText string) {
	if text == "" {
		return nil, text
	}
	if c, clean := tryDSML(text, tools); c != nil {
		return c, clean
	}
	if c, clean := trySentinel(text, tools); c != nil {
		return c, clean
	}
	if jsonFallback && len(tools) > 0 {
		allowed := knownToolNames(tools)
		blobs := bareJSONBlobs(text, allowed)
		if len(blobs) > 0 {
			for _, blob := range blobs {
				if c := parseJSONCalls(blob, tools); c != nil {
					calls = append(calls, c...)
				}
			}
			if len(calls) > 0 {
				clean := text
				for _, blob := range blobs {
					clean = strings.Replace(clean, blob, " ", 1)
				}
				return calls, strings.TrimSpace(clean)
			}
		}
	}
	return nil, text
}

// bareJSONBlobs scans the whole text for JSON objects that look like tool
// calls: a name (or tool) key in the declared whitelist AND an
// arguments/args key. Both criteria are required so code samples and echoed
// tool definitions are never mistaken for calls.
func bareJSONBlobs(text string, allowed map[string]bool) []string {
	var out []string
	i := 0
	for i < len(text) {
		if text[i] != '{' {
			i++
			continue
		}
		blob := scanJSONBlob(text, i)
		if blob == "" {
			break
		}
		if obj, ok := looseJSON(blob).(map[string]any); ok {
			name, _ := obj["name"].(string)
			if name == "" {
				name, _ = obj["tool"].(string)
			}
			if name != "" && allowed[name] && (hasKey(obj, "arguments") || hasKey(obj, "args")) {
				out = append(out, blob)
				i += len(blob)
				continue
			}
		}
		i++
	}
	return out
}

func hasKey(m map[string]any, k string) bool {
	_, ok := m[k]
	return ok
}

// scanJSONBlob finds the first '{' at/after start and returns the
// brace-balanced object, keeping string-literal braces out of the count.
// When the stream is cut short it returns what was seen, for looseJSON to
// repair.
func scanJSONBlob(text string, start int) string {
	s := strings.Index(text[start:], "{")
	if s < 0 {
		return ""
	}
	s += start
	depth := 0
	inStr, esc := false, false
	for i := s; i < len(text); i++ {
		ch := text[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case ch == '\\':
				esc = true
			case ch == '"':
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[s : i+1]
			}
		}
	}
	return text[s:]
}
