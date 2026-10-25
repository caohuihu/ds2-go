package handlers

import (
	"encoding/json"
	"strings"
)

// streamingToolDetector is the stateful stream-side tool-call detector
// (port of webai2api's StreamingToolDetector).
//
// States:
//
//	SCAN     — scan for DSML / sentinel markers; tail may start a marker
//	DSML     — accumulate until the earliest closing tag, then parse
//	SENTINEL — accumulate until a closing sentinel
//
// Zero-artifact tolerance: once inside a marker, the only exits are
// "parsed tool_calls" or "silently discarded". Orphan closing tags seen
// in SCAN are stripped and never emitted into content.

type streamActionKind int

const (
	actionText streamActionKind = iota
	actionToolCalls
)

type streamAction struct {
	kind  streamActionKind
	text  string
	calls []ToolCall
}

type detState int

const (
	detScan detState = iota
	detDSML
	detSentinel
)

type streamingToolDetector struct {
	tools         []toolDef
	requireMarker bool // no declared tools: only explicit markers parse; orphan tags pass as text
	state         detState
	buf           string
	closed        bool
}

func newStreamingToolDetector(tools []toolDef, requireMarker bool) *streamingToolDetector {
	return &streamingToolDetector{tools: tools, requireMarker: requireMarker, state: detScan}
}

// feed consumes one content delta and returns the client-visible actions.
func (d *streamingToolDetector) feed(delta string) []streamAction {
	if d.closed {
		if delta == "" {
			return nil
		}
		return []streamAction{{kind: actionText, text: delta}}
	}
	d.buf += delta
	var actions []streamAction
	guard := 0
	for d.buf != "" && guard < 256 {
		guard++
		switch d.state {
		case detDSML:
			if !d.stepDSML(&actions) {
				return actions
			}
		case detSentinel:
			if !d.stepSentinel(&actions) {
				return actions
			}
		default:
			if !d.stepScan(&actions) {
				return actions
			}
		}
	}
	return actions
}

// finalize flushes whatever remains when the upstream stream ends.
func (d *streamingToolDetector) finalize() []streamAction {
	var actions []streamAction
	if d.buf == "" {
		d.closed = true
		return actions
	}

	switch d.state {
	case detDSML:
		body := d.buf
		if idx := dsmlCallsOpenRE.FindStringIndex(body); idx != nil && idx[0] == 0 {
			body = body[idx[1]:]
		}
		if calls := parseDSMLBody(body, d.tools); len(calls) > 0 {
			actions = append(actions, streamAction{kind: actionToolCalls, calls: calls})
		}
		// unparseable residue is discarded, never leaked as content
	case detSentinel:
		text := d.buf
		for _, v := range openVariants {
			if strings.HasPrefix(text, v) {
				text = text[len(v):]
				break
			}
		}
		if calls := parseSentinelBody(text, d.tools); len(calls) > 0 {
			actions = append(actions, streamAction{kind: actionToolCalls, calls: calls})
		}
	default:
		actions = append(actions, streamAction{kind: actionText, text: d.buf})
	}

	d.buf = ""
	d.state = detScan
	d.closed = true
	return actions
}

// ---- SCAN ----

func (d *streamingToolDetector) stepScan(actions *[]streamAction) bool {
	if d.buf == "" {
		return false
	}

	idx, kind := d.findEarliestScanToken()
	if idx < 0 {
		safe := d.safePrefixLen()
		if safe > 0 {
			*actions = append(*actions, streamAction{kind: actionText, text: d.buf[:safe]})
			d.buf = d.buf[safe:]
		}
		return false
	}

	if idx > 0 {
		prefix := d.buf[:idx]
		// strip orphan closing tags from the prefix before emitting
		if dsmlNoiseRE.MatchString(prefix) {
			prefix = dsmlNoiseRE.ReplaceAllString(prefix, "")
		}
		if prefix != "" {
			*actions = append(*actions, streamAction{kind: actionText, text: prefix})
		}
		d.buf = d.buf[idx:]
	}

	switch kind {
	case "dsml_open":
		d.state = detDSML
		return true
	case "dsml_close":
		// orphan closing tag: strip, never emit
		if m := dsmlNoiseRE.FindStringIndex(d.buf); m != nil && m[0] == 0 {
			d.buf = d.buf[m[1]:]
			return true
		}
		return false
	case "sentinel_open":
		d.state = detSentinel
		return true
	}
	return false
}

func (d *streamingToolDetector) findEarliestScanToken() (int, string) {
	bestIdx, bestKind := -1, ""

	for _, v := range openVariants {
		if i := strings.Index(d.buf, v); i >= 0 && (bestIdx < 0 || i < bestIdx) {
			bestIdx, bestKind = i, "sentinel_open"
		}
	}

	if m := dsmlAnyOpenRE.FindStringIndex(d.buf); m != nil && (bestIdx < 0 || m[0] < bestIdx) {
		bestIdx, bestKind = m[0], "dsml_open"
	}

	// orphan closing tags are only stripped in tools mode; without tools
	// they stay ordinary text
	if !d.requireMarker {
		if m := dsmlNoiseRE.FindStringIndex(d.buf); m != nil && (bestIdx < 0 || m[0] < bestIdx) {
			bestIdx, bestKind = m[0], "dsml_close"
		}
	}

	return bestIdx, bestKind
}

// safePrefixLen returns how much of buf can be emitted as plain text.
// A trailing unclosed "<..." may still become a marker tag, keep it.
func (d *streamingToolDetector) safePrefixLen() int {
	buf := d.buf
	n := len(buf)
	if n == 0 {
		return 0
	}
	lower := n - safeKeep
	if lower < 0 {
		lower = 0
	}
	for i := n - 1; i >= lower; i-- {
		if buf[i] == '<' {
			if !strings.Contains(buf[i:], ">") {
				return i
			}
			break
		}
	}
	return n
}

// ---- DSML ----

func (d *streamingToolDetector) stepDSML(actions *[]streamAction) bool {
	endPos, endLen := -1, 0

	if m := dsmlCallsCloseRE.FindStringIndex(d.buf); m != nil {
		endPos, endLen = m[0], m[1]-m[0]
	}
	if m := dsmlInvokeCloseRE.FindStringIndex(d.buf); m != nil && (endPos < 0 || m[0] < endPos) {
		endPos, endLen = m[0], m[1]-m[0]
	}
	if endPos < 0 {
		return false
	}

	body := d.buf[:endPos]
	rest := d.buf[endPos+endLen:]

	if idx := dsmlCallsOpenRE.FindStringIndex(body); idx != nil && idx[0] == 0 {
		body = body[idx[1]:]
	}

	if calls := parseDSMLBody(body, d.tools); len(calls) > 0 {
		*actions = append(*actions, streamAction{kind: actionToolCalls, calls: calls})
	}

	d.buf = rest
	d.state = detScan
	return true
}

// ---- SENTINEL ----

func (d *streamingToolDetector) stepSentinel(actions *[]streamAction) bool {
	start := 0
	for _, v := range openVariants {
		if strings.HasPrefix(d.buf, v) {
			start = len(v)
			break
		}
	}
	bestIdx, bestV := -1, ""
	for _, v := range closeVariants {
		if i := strings.Index(d.buf[start:], v); i >= 0 {
			if bestIdx < 0 || start+i < bestIdx {
				bestIdx, bestV = start+i, v
			}
		}
	}
	if bestIdx < 0 {
		return false
	}

	openV := ""
	for _, v := range openVariants {
		if strings.HasPrefix(d.buf, v) {
			openV = v
			break
		}
	}
	if openV == "" {
		*actions = append(*actions, streamAction{kind: actionText, text: d.buf})
		d.buf = ""
		d.state = detScan
		return true
	}

	body := d.buf[len(openV):bestIdx]
	rest := d.buf[bestIdx+len(bestV):]

	if calls := parseSentinelBody(body, d.tools); len(calls) > 0 {
		*actions = append(*actions, streamAction{kind: actionToolCalls, calls: calls})
	}

	d.buf = rest
	d.state = detScan
	return true
}

// ---- history rendering / canonicalization ----

// renderToolCallsSentinel turns an echoed assistant tool_calls array back
// into the sentinel text form the model was taught, so follow-up prompts
// keep the tool conversation coherent on a fresh session.
func renderToolCallsSentinel(raw json.RawMessage) string {
	var tcs []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &tcs) != nil || len(tcs) == 0 {
		return ""
	}
	lines := []string{toolOpenPlural}
	for _, tc := range tcs {
		if tc.Function.Name == "" {
			continue
		}
		argsAny := map[string]any{}
		if tc.Function.Arguments != "" {
			if json.Unmarshal([]byte(tc.Function.Arguments), &argsAny) != nil {
				argsAny = map[string]any{"value": tc.Function.Arguments}
			}
		}
		line, _ := json.Marshal(map[string]any{"name": tc.Function.Name, "arguments": argsAny})
		lines = append(lines, string(line))
	}
	lines = append(lines, toolClosePlural)
	return strings.Join(lines, "\n")
}

// canonicalToolCall is the stable wire shape used both for the response's
// tool_calls array and for prefix hashing, so client echoes always match.
type canonicalToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

func canonicalCall(c ToolCall) canonicalToolCall {
	var cc canonicalToolCall
	cc.ID = c.ID
	cc.Type = "function"
	cc.Function.Name = c.Name
	cc.Function.Arguments = c.Arguments
	return cc
}

// toolCallsJSON is the exact JSON array sent to the client.
func toolCallsJSON(calls []ToolCall) json.RawMessage {
	arr := make([]canonicalToolCall, 0, len(calls))
	for _, c := range calls {
		arr = append(arr, canonicalCall(c))
	}
	return mustMarshal(arr)
}

// openAIToolCalls renders calls as the response's message.tool_calls array.
func openAIToolCalls(calls []ToolCall) []any {
	out := make([]any, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.toOpenAI())
	}
	return out
}

// canonicalToolCalls normalizes an echoed tool_calls array into a stable
// string for the prefix hash. Clients may (re)serialize it with different
// key orders, whitespace, or even arguments as an embedded object instead
// of the spec's JSON string; all forms must collapse to one key.
func canonicalToolCalls(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var arr []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &arr) != nil {
		return strings.TrimSpace(string(raw))
	}
	out := make([]string, 0, len(arr))
	for _, c := range arr {
		typ := c.Type
		if typ == "" {
			typ = "function"
		}
		norm := ""
		if len(c.Function.Arguments) > 0 {
			var inner any
			if json.Unmarshal(c.Function.Arguments, &inner) == nil {
				switch v := inner.(type) {
				case string:
					norm = v
				default:
					norm = mustMarshalString(v)
				}
			} else {
				norm = strings.TrimSpace(string(c.Function.Arguments))
			}
		}
		out = append(out, c.ID+"\x1f"+typ+"\x1f"+c.Function.Name+"\x1f"+norm)
	}
	return strings.Join(out, "\x1e")
}
