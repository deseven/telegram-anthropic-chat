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
	// Memories are rendered as bullet lines ("- text"); extract them in order.
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "- ") {
			lines = append(lines, strings.TrimPrefix(l, "- "))
		}
	}
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
	// Zero date => 1970-01-01 (Thursday). Both memories share that day, so a
	// single header is emitted followed by two bullet points.
	ms := []storage.Memory{
		{ID: 1, Text: "one"},
		{ID: 2, Text: "two"},
	}
	want := "Thursday, 01 Jan 1970\n- one\n- two"
	if out := Render(ms); out != want {
		t.Fatalf("expected grouped list, got %q", out)
	}
}

// TestRenderGroupsByDate verifies that memories from different days are split
// into separate date headers, each followed by its bullet-pointed memories.
func TestRenderGroupsByDate(t *testing.T) {
	day1 := dayStart(time.Date(2026, 7, 15, 0, 0, 0, 0, time.UTC)) // Wednesday
	day2 := dayStart(time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)) // Thursday
	ms := []storage.Memory{
		{ID: 1, Text: "Some memory from Wednesday", Date: day1},
		{ID: 2, Text: "Another memory from Wednesday", Date: day1},
		{ID: 3, Text: "Some memory from Thursday", Date: day2},
		{ID: 4, Text: "Another memory from Thursday", Date: day2},
	}
	want := "Wednesday, 15 Jul 2026\n" +
		"- Some memory from Wednesday\n" +
		"- Another memory from Wednesday\n" +
		"\n" +
		"Thursday, 16 Jul 2026\n" +
		"- Some memory from Thursday\n" +
		"- Another memory from Thursday"
	if out := Render(ms); out != want {
		t.Fatalf("expected date-grouped list, got %q", out)
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
// alone exhaust their (capped) share of the budget, they fill no more than
// that share — older memories are not starved out, and only an older memory
// that fits the actual remainder is included.
func TestSplitRecentDayBudgetExhausted(t *testing.T) {
	today := dayStart(time.Now())
	old := today - 86400 // yesterday
	ms := []storage.Memory{
		{ID: 1, Importance: 1, Text: "fresh one", Date: today},   // 9+1=10
		{ID: 2, Importance: 1, Text: "fresh two", Date: today},   // 9+1=10
		{ID: 3, Importance: 10, Text: "old critical", Date: old}, // 12+1=13
	}
	// Budget 20: the fresh share is capped at 2/3 (13), so only the first
	// fresh memory fits (10). "old critical" needs 13 more (23 > 20), so it
	// stays out despite its importance — the cap reserves room, but a memory
	// still has to fit the true remaining budget.
	in, out := Split(ms, 20)
	if len(in) != 1 || in[0].ID != 1 {
		t.Fatalf("expected only id 1 in-context, got %+v", in)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 remaining, got %d: %+v", len(out), out)
	}
}

// TestSplitFreshCapProtectsOlder verifies the monster-day scenario: a huge
// fresh day may occupy at most ~2/3 of the budget, so the highest-importance
// older memories always keep their context slot.
func TestSplitFreshCapProtectsOlder(t *testing.T) {
	today := dayStart(time.Now())
	old := today - 86400
	ms := []storage.Memory{
		{ID: 1, Importance: 10, Text: "old critical", Date: old}, // 12+1=13
		{ID: 2, Importance: 1, Text: "fresh one", Date: today},   // 9+1=10
		{ID: 3, Importance: 1, Text: "fresh two", Date: today},   // 9+1=10
		{ID: 4, Importance: 1, Text: "fresh three", Date: today}, // 11+1=12
	}
	// Budget 33: fresh cap is 2/3 (22), so fresh picks id 2 (10) and id 3
	// (10) — id 4 (12) would make 32 > 22. Older gets the rest: 33-20=13,
	// exactly fitting "old critical" (13).
	in, out := Split(ms, 33)
	want := []int{1, 2, 3}
	if len(in) != len(want) {
		t.Fatalf("expected %d in-context, got %+v", len(want), in)
	}
	for i, w := range want {
		if in[i].ID != w {
			t.Fatalf("in[%d].ID = %d, want %d (full in: %+v)", i, in[i].ID, w, in)
		}
	}
	if len(out) != 1 || out[0].ID != 4 {
		t.Fatalf("expected remaining to be id 4, got %+v", out)
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
// memories don't all fit (within their capped share), they are included in
// historical order until the cap runs out.
func TestSplitRecentMemoriesDontFitAll(t *testing.T) {
	today := dayStart(time.Now())
	old := today - 86400
	ms := []storage.Memory{
		{ID: 1, Importance: 1, Text: "first fresh memory", Date: today},  // 19+1=20
		{ID: 2, Importance: 1, Text: "second fresh memory", Date: today}, // 20+1=21
		{ID: 3, Importance: 10, Text: "old", Date: old},                  // 3+1=4
	}
	// Budget 20: fresh cap is 2/3 (13), which fits neither fresh memory (20,
	// 21), so the older "old" (4) is the only pick.
	in, out := Split(ms, 20)
	if len(in) != 1 || in[0].ID != 3 {
		t.Fatalf("expected only id 3 in-context, got %+v", in)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 remaining, got %d: %+v", len(out), out)
	}
}

// --- Stale pruning tests ---

// testUnit is the per-importance retention unit used in the tests below:
// a memory is stale once it hasn't reached the context for importance*testUnit.
const testUnit = 24 * time.Hour

// TestStaleDropsOldOverflowOnly is the central safety test: only memories that
// are BOTH out-of-context (did not fit the budget) AND past their
// importance-scaled retention are returned as stale. In-context memories (even
// ancient ones) and recently-used out-of-context memories are never pruned.
func TestStaleDropsOldOverflowOnly(t *testing.T) {
	now := time.Now()
	recentDay := dayStart(now.Add(-5 * 24 * time.Hour))   // most recent active day
	recentOld := now.Add(-10 * 24 * time.Hour).Unix()     // older partition, within retention
	ancientDay := now.Add(-40 * 24 * time.Hour).Unix()    // older partition, past retention

	// Budget is tight: only the fresh memory (id 1) and the highest-importance
	// older memory (id 2) fit. The rest overflow.
	//   id 1: "R"  -> 1+1 = 2  (fresh, fits)
	//   id 2: "OF" -> 2+1 = 3  (older, imp 9, fits)      => ancient but IN context (and
	//                                                       used 1 day ago, so within 9*1d)
	//   id 3: "OS" -> 2+1 = 3  (older, imp 1, overflows)  => unused for 40d > 1*1d = STALE
	//   id 4: "RO" -> 2+1 = 3  (older, imp 1, overflows)  => unused for 10d > 1*1d = STALE
	//   id 5: "RN" -> 2+1 = 3  (older, imp 9, overflows)  => unused for 10d < 9*1d = kept
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "R", Date: recentDay},
		{ID: 2, Importance: 9, Text: "OF", Date: ancientDay, LastUsed: now.Add(-24 * time.Hour).Unix()},
		{ID: 3, Importance: 1, Text: "OS", Date: ancientDay},
		{ID: 4, Importance: 1, Text: "RO", Date: recentOld},
		{ID: 5, Importance: 9, Text: "RN", Date: recentOld},
	}
	// Budget 5: fresh cap = 3 (2/3 of 5), so id1 (2) fits; the remaining 3
	// chars fit the top-ranked older memory, id2 (3). id3/id4/id5 overflow.
	stale := Stale(ms, 5, testUnit, now)
	if len(stale) != 2 {
		t.Fatalf("expected 2 stale memories, got %d: %+v", len(stale), stale)
	}
	if stale[0].ID != 3 || stale[1].ID != 4 {
		t.Fatalf("expected stale ids 3 and 4, got %+v", stale)
	}
}

// TestStaleNeverReturnsInContext verifies the key invariant: a memory that fits
// the budget is never stale, no matter how old it is.
func TestStaleNeverReturnsInContext(t *testing.T) {
	now := time.Now()
	ancient := dayStart(now.Add(-40 * 24 * time.Hour))
	// An ancient, high-importance memory that fits the budget.
	ms := []storage.Memory{
		{ID: 1, Importance: 10, Text: "ancient but fits", Date: ancient},
	}
	stale := Stale(ms, 1000, testUnit, now)
	if len(stale) != 0 {
		t.Fatalf("in-context memory must never be stale, got %+v", stale)
	}
}

// TestStaleAllFitNothingStale verifies that when everything fits (empty out
// partition), nothing is stale even if all memories are ancient.
func TestStaleAllFitNothingStale(t *testing.T) {
	now := time.Now()
	ancient := dayStart(now.Add(-40 * 24 * time.Hour))
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "ancient one", Date: ancient},
		{ID: 2, Importance: 5, Text: "ancient two", Date: ancient},
	}
	stale := Stale(ms, 1000, testUnit, now)
	if len(stale) != 0 {
		t.Fatalf("expected no stale when all fit, got %+v", stale)
	}
}

// TestStaleRecentOverflowNotStale verifies that out-of-context memories within
// their importance-scaled retention window are retained (not stale).
func TestStaleRecentOverflowNotStale(t *testing.T) {
	now := time.Now()
	recent := now.Add(-3 * 24 * time.Hour).Unix()
	// All on the same day 3 days ago (the most recent active day). A tiny
	// budget fits only the first memory; the others overflow but were "used"
	// 3 days ago: importance 5 gives them 5 days of retention, so they must
	// not be pruned.
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "recent fits", Date: recent},
		{ID: 2, Importance: 5, Text: "recent overflow", Date: recent},
		{ID: 3, Importance: 5, Text: "imp five overflow", Date: recent},
	}
	stale := Stale(ms, len("recent fits")+1, testUnit, now)
	if len(stale) != 0 {
		t.Fatalf("recent overflow must not be stale, got %+v", stale)
	}
}

// TestStaleImportanceScaling verifies that the retention period scales with
// importance: at 25 unused days, memories with importance 1/5/10 (retention
// 1/5/10 days) are stale while a (hypothetically clamped) importance-30 memory
// (30-day retention) with the same usage date is kept.
func TestStaleImportanceScaling(t *testing.T) {
	now := time.Now()
	old := now.Add(-25 * 24 * time.Hour).Unix()
	// All on the same day (25 days ago), none fits the budget.
	ms := []storage.Memory{
		{ID: 1, Importance: 1, Text: "one", Date: old},     // retention 1*1d: stale
		{ID: 2, Importance: 5, Text: "five", Date: old},    // retention 5*1d: stale
		{ID: 3, Importance: 10, Text: "ten", Date: old},    // retention 10*1d: stale
		{ID: 4, Importance: 30, Text: "thirty", Date: old}, // retention 30*1d: kept
	}
	stale := Stale(ms, 1, testUnit, now)
	if len(stale) != 3 {
		t.Fatalf("expected 3 stale memories, got %d: %+v", len(stale), stale)
	}
	for i, w := range []int{1, 2, 3} {
		if stale[i].ID != w {
			t.Fatalf("stale[%d].ID = %d, want %d (full: %+v)", i, stale[i].ID, w, stale)
		}
	}
}

// TestStaleLastUsedRefreshes verifies that retention is measured from LastUsed,
// not Date: an ancient memory that recently reached the context is kept, while
// a recent memory that never did is pruned.
func TestStaleLastUsedRefreshes(t *testing.T) {
	now := time.Now()
	ancient := now.Add(-300 * 24 * time.Hour).Unix()
	recent := now.Add(-2 * 24 * time.Hour).Unix()
	ms := []storage.Memory{
		// Created 300 days ago but used 1 day ago: retention 5*1d => kept.
		{ID: 1, Importance: 5, Text: "ancient but used", Date: ancient, LastUsed: now.Add(-24 * time.Hour).Unix()},
		// Created 2 days ago, never used since (LastUsed = Date): retention 1*1d => stale.
		{ID: 2, Importance: 1, Text: "recent but unused", Date: recent},
	}
	// Put them on different days so Split treats them independently; tiny
	// budget so both overflow. Same day would make id 2 "fresh".
	stale := Stale(ms, 1, testUnit, now)
	if len(stale) != 1 || stale[0].ID != 2 {
		t.Fatalf("expected only id 2 stale (LastUsed-based), got %+v", stale)
	}
}

// TestStaleBoundary verifies that a memory exactly at its retention threshold
// is NOT stale (only strictly older memories are pruned).
func TestStaleBoundary(t *testing.T) {
	now := time.Now()
	exactly := now.Add(-5 * testUnit).Unix() // exactly importance*unit ago
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "boundary overflow", Date: exactly},
	}
	// Doesn't fit (budget too small) but exactly at threshold => not stale.
	stale := Stale(ms, 1, testUnit, now)
	if len(stale) != 0 {
		t.Fatalf("memory exactly at threshold must not be stale, got %+v", stale)
	}
}

// TestStaleJustOverBoundary verifies that a memory just past its retention
// threshold that also overflows IS stale.
func TestStaleJustOverBoundary(t *testing.T) {
	now := time.Now()
	over := now.Add(-5*testUnit - time.Second).Unix() // one second past threshold
	ms := []storage.Memory{
		{ID: 1, Importance: 5, Text: "just over overflow", Date: over},
	}
	stale := Stale(ms, 1, testUnit, now)
	if len(stale) != 1 || stale[0].ID != 1 {
		t.Fatalf("memory just past threshold + overflow must be stale, got %+v", stale)
	}
}

// TestStaleDisabled verifies that ageUnit <= 0 disables pruning entirely, even
// for ancient overflowing memories.
func TestStaleDisabled(t *testing.T) {
	now := time.Now()
	ancient := dayStart(now.Add(-40 * 24 * time.Hour))
	ms := []storage.Memory{
		{ID: 1, Importance: 1, Text: "ancient overflow", Date: ancient},
	}
	stale := Stale(ms, 1, 0, now)
	if len(stale) != 0 {
		t.Fatalf("ageUnit<=0 must disable pruning, got %+v", stale)
	}
}

// TestStaleEmpty verifies that an empty (or nil) input yields no stale memories.
func TestStaleEmpty(t *testing.T) {
	if stale := Stale(nil, 100, testUnit, time.Now()); len(stale) != 0 {
		t.Fatalf("expected no stale for nil input, got %+v", stale)
	}
}
