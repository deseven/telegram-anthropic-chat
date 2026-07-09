package memories

import (
	"strings"
	"testing"
	"time"

	"github.com/zoo/telegram-anthropic-chat/internal/storage"
)

// dayStart returns the UTC start-of-day Unix timestamp for the given time.
func dayStart(t time.Time) int64 {
	return t.UTC().Truncate(24 * time.Hour).Unix()
}

// TestSelectOrderingAndLimit verifies that with a tight budget, lower-importance
// older memories are dropped while higher-importance ones are kept, and the
// final order is by id ascending.
func TestSelectOrderingAndLimit(t *testing.T) {
	// All memories on the same (most recent) day: they are all "fresh" and
	// included in historical order until the budget runs out.
	today := dayStart(time.Now())
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "oldest low importance", Date: today},
		{ID: 2, Importance: 9, Text: "important recent", Date: today},
		{ID: 3, Importance: 8, Text: "also important", Date: today},
		{ID: 4, Importance: 2, Text: "trivial", Date: today},
	}
	// Budget fits the first three (53 chars + 3 newlines = 56).
	out := Select(ms, 56)
	lines := strings.Split(out, "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 picked memories, got %d: %q", len(lines), out)
	}
	want := []string{"oldest low importance", "important recent", "also important"}
	for i, w := range want {
		if lines[i] != w {
			t.Fatalf("line %d = %q, want %q (full: %q)", i, lines[i], w, out)
		}
	}
}

func TestSelectCtxSizeLimit(t *testing.T) {
	today := dayStart(time.Now())
	ms := []storage.Memory{
		{ID: 1, Importance: 10, Text: "short", Date: today},
		{ID: 2, Importance: 9, Text: "another", Date: today},
	}
	// Only room for one short line (text + newline = 6 chars).
	out := Select(ms, len("short")+1)
	if !strings.Contains(out, "short") {
		t.Fatalf("expected 'short' to be picked, got %q", out)
	}
	if strings.Contains(out, "another") {
		t.Fatalf("'another' should not fit, got %q", out)
	}
}

func TestSelectEmpty(t *testing.T) {
	if out := Select(nil, 1000); out != "" {
		t.Fatalf("expected empty, got %q", out)
	}
}

func TestSplitAllFit(t *testing.T) {
	today := dayStart(time.Now())
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "alpha", Date: today},
		{ID: 2, Importance: 9, Text: "beta", Date: today},
	}
	in, out := Split(ms, 1000)
	if len(in) != 2 {
		t.Fatalf("expected 2 in-context, got %d: %+v", len(in), in)
	}
	if len(out) != 0 {
		t.Fatalf("expected 0 remaining, got %d: %+v", len(out), out)
	}
	if in[0].ID != 1 || in[1].ID != 2 {
		t.Fatalf("unexpected in order: %+v", in)
	}
}

func TestSplitSomeDropped(t *testing.T) {
	// All on the most recent day: the lowest-importance one is dropped when
	// the budget is tight, and the rest stay in id-ascending order.
	today := dayStart(time.Now())
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "oldest low importance", Date: today},
		{ID: 2, Importance: 9, Text: "important recent", Date: today},
		{ID: 3, Importance: 8, Text: "also important", Date: today},
		{ID: 4, Importance: 2, Text: "trivial", Date: today},
	}
	in, out := Split(ms, 56)
	if len(in) != 3 {
		t.Fatalf("expected 3 in-context, got %d: %+v", len(in), in)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 remaining, got %d: %+v", len(out), out)
	}
	if out[0].ID != 4 {
		t.Fatalf("expected remaining to be the trivial one, got %+v", out)
	}
	want := []int{1, 2, 3}
	for i, w := range want {
		if in[i].ID != w {
			t.Fatalf("in[%d].ID = %d, want %d", i, in[i].ID, w)
		}
	}
}

func TestSplitEmpty(t *testing.T) {
	in, out := Split(nil, 1000)
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
	const ts = 1752019200 // 2025-07-09 00:00:00 UTC
	ms := []storage.Memory{
		{ID: 7, Text: "alpha", Date: ts},
		{ID: 42, Text: "beta", Date: ts},
	}
	want := "**#7** (2025-07-09) alpha  \n**#42** (2025-07-09) beta"
	if out := RenderList(ms); out != want {
		t.Fatalf("expected id+date list, got %q", out)
	}
}

// --- Most-recent-day priority tests ---

// TestSplitRecentDayFirst verifies that memories from the most recent day are
// always included (in historical order) before older memories compete for the
// remaining budget by importance.
func TestSplitRecentDayFirst(t *testing.T) {
	day1 := dayStart(time.Date(2025, 7, 7, 0, 0, 0, 0, time.UTC))
	day2 := dayStart(time.Date(2025, 7, 9, 0, 0, 0, 0, time.UTC)) // most recent day
	ms := []storage.Memory{
		{ID: 1, Importance: 9, Text: "old important", Date: day1},     // 14+1=15
		{ID: 2, Importance: 2, Text: "fresh trivial", Date: day2},    // 13+1=14
		{ID: 3, Importance: 3, Text: "freshest trivial", Date: day2}, // 16+1=17
		{ID: 4, Importance: 10, Text: "ancient critical", Date: day1}, // 16+1=17
	}

	// Budget: fresh memories (14+17=31) + older "ancient critical" (17) = 48.
	// "old important" (15) would overflow 48+15=63, so it is dropped despite
	// higher importance than the fresh trivial ones.
	in, out := Split(ms, 48)

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

// TestSplitRecentDayBudgetExhausted verifies that when recent-day memories
// alone exhaust the budget, no older memories are included.
func TestSplitRecentDayBudgetExhausted(t *testing.T) {
	today := dayStart(time.Now())
	old := today - 86400 // yesterday
	ms := []storage.Memory{
		{ID: 1, Importance: 1, Text: "fresh one", Date: today},   // 9+1=10
		{ID: 2, Importance: 1, Text: "fresh two", Date: today},   // 9+1=10
		{ID: 3, Importance: 10, Text: "old critical", Date: old}, // 12+1=13
	}
	// Budget only fits the two fresh memories (20 chars).
	in, out := Split(ms, 20)
	if len(in) != 2 {
		t.Fatalf("expected 2 in-context (fresh only), got %d: %+v", len(in), in)
	}
	if len(out) != 1 || out[0].ID != 3 {
		t.Fatalf("expected remaining to be id 3, got %+v", out)
	}
	if in[0].ID != 1 || in[1].ID != 2 {
		t.Fatalf("unexpected in order: %+v", in)
	}
}

// TestSplitOlderImportanceThenRecency verifies that older memories are ranked by
// importance descending, and within the same importance by id descending (most
// recent first), so the most recent important memories win ties.
func TestSplitOlderImportanceThenRecency(t *testing.T) {
	old := dayStart(time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC))
	today := dayStart(time.Now())
	// Three older memories with the same importance; only two fit the budget.
	// id 3 (most recent) and id 2 should be picked over id 1.
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "old one", Date: old},   // 7+1=8
		{ID: 2, Importance: 5, Text: "old two", Date: old},  // 7+1=8
		{ID: 3, Importance: 5, Text: "old three", Date: old}, // 9+1=10
		{ID: 4, Importance: 1, Text: "fresh", Date: today},   // 5+1=6
	}
	// Budget: fresh (6) + two older (8+8=16) = 30. The third older (10) overflows.
	in, out := Split(ms, 30)
	if len(in) != 3 {
		t.Fatalf("expected 3 in-context, got %d: %+v", len(in), in)
	}
	// In-context sorted by id asc: older picked (2,3) then fresh (4) => [2,3,4]
	want := []int{2, 3, 4}
	for i, w := range want {
		if in[i].ID != w {
			t.Fatalf("in[%d].ID = %d, want %d (full in: %+v)", i, in[i].ID, w, in)
		}
	}
	if len(out) != 1 || out[0].ID != 1 {
		t.Fatalf("expected remaining to be id 1, got %+v", out)
	}
}

// TestSplitRecentMemoriesDontFitAll verifies that if even the recent-day
// memories don't all fit, they are included in historical order until the
// budget runs out, and nothing older is added.
func TestSplitRecentMemoriesDontFitAll(t *testing.T) {
	today := dayStart(time.Now())
	old := today - 86400
	ms := []storage.Memory{
		{ID: 1, Importance: 1, Text: "first fresh memory", Date: today},  // 19+1=20
		{ID: 2, Importance: 1, Text: "second fresh memory", Date: today}, // 20+1=21
		{ID: 3, Importance: 10, Text: "old", Date: old},                  // 3+1=4
	}
	// Budget fits only the first fresh memory (20). The second (21) would
	// overflow, and "old" has no room either.
	in, out := Split(ms, 20)
	if len(in) != 1 || in[0].ID != 1 {
		t.Fatalf("expected only id 1 in-context, got %+v", in)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 remaining, got %d: %+v", len(out), out)
	}
}
