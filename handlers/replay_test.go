package handlers

import (
	"testing"
)

func msgs(roles ...string) []chatMessage {
	out := make([]chatMessage, 0, len(roles))
	for _, r := range roles {
		out = append(out, chatMessage{Role: r})
	}
	return out
}

// TestReplayStartIndex: user messages are the only seeding cut points, so
// the slice never falls in the middle of an assistant/tool chain.
func TestReplayStartIndex(t *testing.T) {
	// user boundaries at 0, 2, 5; an assistant+tool chain between 2 and 5.
	full := msgs("user", "assistant", "user", "assistant", "tool", "user")

	cases := []struct {
		name string
		n    int
		msgs []chatMessage
		want int
	}{
		{"-1 is the legacy full replay", -1, full, 0},
		{"0 seeds the current instruction only", 0, full, 5},
		{"1 turn group keeps the previous user turn and its chain", 1, full, 2},
		{"2 groups covers everything available", 2, full, 0},
		{"more groups than exist clamps to 0", 9, full, 0},
		{"empty slice is safe", 2, nil, 0},
		{"single message: 0 = that message", 0, msgs("user"), 0},
		{"no user messages: nothing to cut on", 1, msgs("system", "assistant"), 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := replayStartIndex(c.msgs, c.n); got != c.want {
				t.Fatalf("replayStartIndex(n=%d) = %d, want %d", c.n, got, c.want)
			}
		})
	}
}

// TestReplayStartIndexKeepsToolChainsIntact: for every allowed n the
// seeded tail messages[start:] must never drop a tool result whose
// assistant tool_calls call was kept (or vice versa).
func TestReplayStartIndexKeepsToolChainsIntact(t *testing.T) {
	// A tool chain: assistant(1) calls tool, tool(2) is its result, so 1
	// and 2 must travel together into the seeded prompt.
	full := msgs("user", "assistant", "tool", "user", "assistant", "tool", "user")
	for n := 0; n <= 5; n++ {
		start := replayStartIndex(full, n)
		tail := full[start:]
		for i, m := range tail {
			if m.Role == "tool" {
				// its call must be present in the same slice
				found := false
				for j := 0; j < i; j++ {
					if tail[j].Role == "assistant" {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("n=%d: tool result at slice pos %d has no assistant call above it (start=%d)", n, i, start)
				}
			}
		}
	}
}
