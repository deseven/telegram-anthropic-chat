package memories

import (
	"strings"
	"testing"

	"github.com/zoo/telegram-anthropic-chat/internal/storage"
)

// noRecent is an empty session list: all memories are treated as "older" and
// selected purely by importance, matching the legacy behaviour.
var noRecent []string

func TestSelectOrderingAndLimit(t *testing.T) {
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "oldest low importance"},
		{ID: 2, Importance: 9, Text: "important recent"},
		{ID: 3, Importance: 8, Text: "also important"},
		{ID: 4, Importance: 2, Text: "trivial"},
	}
	// With a tight budget, the trivial (importance 2) memory is dropped.
	// The three higher-importance texts total 53 chars + 3 newlines = 56.
	out := Select(ms, 56, noRecent)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 picked memories (trivial dropped by importance), got %d: %q", len(lines), out)
	}
	// Picked (importance>=5) sorted by id asc: 1,2,3
	want := []string{"oldest low importance", "important recent", "also important"}
	for i, w := range want {
		if lines[i] != w {
			t.Fatalf("line %d = %q, want %q (full: %q)", i, lines[i], w, out)
		}
	}
}

func TestSelectCtxSizeLimit(t *testing.T) {
	ms := []storage.Memory{
		{ID: 1, Importance: 10, Text: "short"},
		{ID: 2, Importance: 9, Text: "another"},
	}
	// Only room for one short line (text + newline = 6 chars).
	out := Select(ms, len("short")+1, noRecent)
	if !strings.Contains(out, "short") {
		t.Fatalf("expected 'short' to be picked, got %q", out)
	}
	if strings.Contains(out, "another") {
		t.Fatalf("'another' should not fit, got %q", out)
	}
}

func TestSelectEmpty(t *testing.T) {
	if out := Select(nil, 1000, noRecent); out != "" {
		t.Fatalf("expected empty, got %q", out)
	}
}

func TestSplitAllFit(t *testing.T) {
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "alpha"},
		{ID: 2, Importance: 9, Text: "beta"},
	}
	// Budget large enough for both.
	in, out := Split(ms, 1000, noRecent)
	if len(in) != 2 {
		t.Fatalf("expected 2 in-context, got %d: %+v", len(in), in)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 remaining, got %d: %+v", len(out), out)
	}
	// In-context order is by id ascending.
	if in[0].ID != 1 || in[1].ID != 2 {
		t.Fatalf("unexpected in order: %+v", in)
	}
}

func TestSplitSomeDropped(t *testing.T) {
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "oldest low importance"},
		{ID: 2, Importance: 9, Text: "important recent"},
		{ID: 3, Importance: 8, Text: "also important"},
		{ID: 4, Importance: 2, Text: "trivial"},
	}
	// Same budget as TestSelectOrderingAndLimit: only the three
	// higher-importance memories fit (56 chars).
	in, out := Split(ms, 56, noRecent)
	if len(in) != 3 {
		t.Fatalf("expected 3 in-context, got %d: %+v", len(in), in)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 remaining, got %d: %+v", len(out), out)
	}
	if out[0].ID != 4 {
		t.Fatalf("expected remaining to be the trivial one, got %+v", out)
	}
	// In-context sorted by id asc.
	want := []int{1, 2, 3}
	for i, w := range want {
		if in[i].ID != w {
			t.Fatalf("in[%d].ID = %d, want %d", i, in[i].ID, w)
		}
	}
}

func TestSplitEmpty(t *testing.T) {
	in, out := Split(nil, 1000, noRecent)
	if len(in) != 0 || len(out) != 0 {
		t.Fatalf("expected both empty, got in=%+v out=%+v", in, out)
	}
}

func TestRenderEmpty(t *testing.T) {
	if out := Render(nil); out != "" {
		t.Fatalf("expected empty, got %q", out)
	}
}

func TestRenderPlain(t *testing.T) {
	ms := []storage.Memory{
		{ID: 1, Text: "one"},
		{ID: 2, Text: "two"},
	}
	if out := Render(ms); out != "one\ntwo" {
		t.Fatalf("expected 'one\\ntwo', got %q", out)
	}
}

func TestRenderListEmpty(t *testing.T) {
	if out := RenderList(nil); out != "" {
		t.Fatalf("expected empty, got %q", out)
	}
}

func TestRenderListWithIDs(t *testing.T) {
	ms := []storage.Memory{
		{ID: 7, Text: "alpha"},
		{ID: 42, Text: "beta"},
	}
	if out := RenderList(ms); out != "#7: alpha\n#42: beta" {
		t.Fatalf("expected id-prefixed list, got %q", out)
	}
}

// --- Session-priority tests ---

// TestSplitRecentSessionsFirst verifies that memories from recent sessions are
// always included (in historical order) before older memories compete for the
// remaining budget by importance.
func TestSplitRecentSessionsFirst(t *testing.T) {
	const s1, s2, s3 = "session-1", "session-2", "session-3"
	ms := []storage.Memory{
		{ID: 1, SessionUUID: s1, Importance: 9, Text: "old important"},       // 14+1=15
		{ID: 2, SessionUUID: s2, Importance: 2, Text: "fresh trivial"},       // 13+1=14
		{ID: 3, SessionUUID: s3, Importance: 3, Text: "freshest trivial"},    // 16+1=17
		{ID: 4, SessionUUID: "", Importance: 10, Text: "ancient critical"},   // 16+1=17
	}
	// Recent sessions: s2 and s3 (and s1 is NOT in the last-3 list here).
	recent := []string{s2, s3}

	// Budget: fresh memories (14+17=31) + older "ancient critical" (17) = 48.
	// "old important" (15) would overflow 48+15=63, so it is dropped despite
	// higher importance than the fresh trivial ones.
	in, out := Split(ms, 48, recent)

	if len(in) != 3 {
		t.Fatalf("expected 3 in-context, got %d: %+v", len(in), in)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 remaining, got %d: %+v", len(out), out)
	}
	// Order: older important (id asc) prepended to fresh (id asc).
	// Picked older: id 4. Fresh: id 2, 3. => [4, 2, 3]
	want := []int{4, 2, 3}
	for i, w := range want {
		if in[i].ID != w {
			t.Fatalf("in[%d].ID = %d, want %d (full in: %+v)", i, in[i].ID, w, in)
		}
	}
	if out[0].ID != 1 {
		t.Fatalf("expected remaining to be id 1, got %+v", out)
	}
}

// TestSplitRecentSessionsBudgetExhausted verifies that when recent memories
// alone exhaust the budget, no older memories are included.
func TestSplitRecentSessionsBudgetExhausted(t *testing.T) {
	const s1 = "session-1"
	ms := []storage.Memory{
		{ID: 1, SessionUUID: s1, Importance: 1, Text: "fresh one"},   // 9+1=10
		{ID: 2, SessionUUID: s1, Importance: 1, Text: "fresh two"},   // 9+1=10
		{ID: 3, SessionUUID: "", Importance: 10, Text: "old critical"}, // 12+1=13
	}
	// Budget only fits the two fresh memories (20 chars).
	in, out := Split(ms, 20, []string{s1})
	if len(in) != 2 {
		t.Fatalf("expected 2 in-context (fresh only), got %d: %+v", len(in), in)
	}
	if len(out) != 1 || out[0].ID != 3 {
		t.Fatalf("expected remaining to be id 3, got %+v", out)
	}
	// Fresh memories in id asc.
	if in[0].ID != 1 || in[1].ID != 2 {
		t.Fatalf("unexpected in order: %+v", in)
	}
}

// TestSplitNoRecentSessionsFallsBackToImportance verifies that with no recent
// sessions, the selection is purely importance-based (legacy behaviour).
func TestSplitNoRecentSessionsFallsBackToImportance(t *testing.T) {
	ms := []storage.Memory{
		{ID: 1, SessionUUID: "old", Importance: 5, Text: "low"},
		{ID: 2, SessionUUID: "old", Importance: 9, Text: "high"},
	}
	// Budget for one; the higher-importance one wins.
	in, out := Split(ms, len("high")+1, nil)
	if len(in) != 1 || in[0].ID != 2 {
		t.Fatalf("expected only id 2 in-context, got %+v", in)
	}
	if len(out) != 1 || out[0].ID != 1 {
		t.Fatalf("expected id 1 remaining, got %+v", out)
	}
}

// TestSplitRecentMemoriesDontFitAll verifies that if even the recent memories
// don't all fit, they are included in historical order until the budget runs
// out, and nothing older is added.
func TestSplitRecentMemoriesDontFitAll(t *testing.T) {
	const s1 = "session-1"
	ms := []storage.Memory{
		{ID: 1, SessionUUID: s1, Importance: 1, Text: "first fresh memory"},  // 19+1=20
		{ID: 2, SessionUUID: s1, Importance: 1, Text: "second fresh memory"}, // 20+1=21
		{ID: 3, SessionUUID: "", Importance: 10, Text: "old"},                // 3+1=4
	}
	// Budget fits only the first fresh memory (20). The second (21) would
	// overflow, and "old" has no room either.
	in, out := Split(ms, 20, []string{s1})
	if len(in) != 1 || in[0].ID != 1 {
		t.Fatalf("expected only id 1 in-context, got %+v", in)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 remaining, got %d: %+v", len(out), out)
	}
}
