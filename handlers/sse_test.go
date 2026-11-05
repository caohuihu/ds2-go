package handlers

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// flushRecorder is a minimal http.ResponseWriter that also implements
// http.Flusher, recording headers and body so SSE frame sequences can be
// asserted in tests.
type flushRecorder struct {
	header     http.Header
	body       bytes.Buffer
	statusCode int
}

func (f *flushRecorder) Header() http.Header {
	if f.header == nil {
		f.header = http.Header{}
	}
	return f.header
}

func (f *flushRecorder) Write(b []byte) (int, error) { return f.body.Write(b) }

func (f *flushRecorder) WriteHeader(statusCode int) { f.statusCode = statusCode }

func (f *flushRecorder) Flush() {}

// toolIndexSeq scans the emitted SSE frames and returns the tool_call delta
// index of every frame carrying tool_calls, in order. Object keys are
// sorted in each frame, so the choice-level "index" key is serialized AFTER
// the delta object: on a tool frame the FIRST "index" on the line is the
// tool_call's. Frames without tool_calls contribute nothing.
func toolIndexSeq(t *testing.T, body string) []int {
	t.Helper()
	var seq []int
	for _, line := range strings.Split(body, "\n") {
		if !strings.Contains(line, `"tool_calls":`) {
			continue
		}
		i := strings.Index(line, `"index":`)
		if i < 0 {
			t.Fatalf("tool_calls frame without index: %s", line)
		}
		j := i + len(`"index":`)
		k := j
		for k < len(line) && line[k] >= '0' && line[k] <= '9' {
			k++
		}
		n, err := strconv.Atoi(line[j:k])
		if err != nil {
			t.Fatalf("unparseable tool_call index in frame %q", line)
		}
		seq = append(seq, n)
	}
	return seq
}

func intSliceEq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSSEToolCallIndicesContinue: a second marker block flushed by the
// streaming detector must continue the stream-global tool_call numbering
// where the first block ended - 0,1 then 2,3 (each call emits a head frame
// with id+name and an arguments frame, so every index appears twice).
// Restarting at 0 made clients stack every call into slot 0.
func TestSSEToolCallIndicesContinue(t *testing.T) {
	fr := &flushRecorder{}
	s := newSSEWriter(fr, "chatcmpl-test", "deepseek-chat", 1, "conv-test")
	if err := s.open(); err != nil {
		t.Fatalf("open: %v", err)
	}
	s.toolCalls([]ToolCall{{ID: "a", Name: "f", Arguments: "{}"}, {ID: "b", Name: "g", Arguments: "{}"}})
	s.toolCalls([]ToolCall{{ID: "c", Name: "h", Arguments: "{}"}})
	want := []int{0, 0, 1, 1, 2, 2}
	if got := toolIndexSeq(t, fr.body.String()); !intSliceEq(got, want) {
		t.Fatalf("tool_call index sequence = %v, want %v", got, want)
	}
}
