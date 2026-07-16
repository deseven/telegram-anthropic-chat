// Package memories implements memory selection for the chat context.
//
// Selection algorithm:
//  1. Determine the most recent day (UTC) that produced any memory, using
//     each memory's Date field. All memories from that day are "fresh" and are
//     always included first, in historical (id ascending) order. This covers
//     both edge cases: a day with many conversations and a week with a single
//     one — the model always remembers everything from the last active day.
//  2. For the remaining budget, sort all other (older) memories by importance
//     descending, breaking ties by id descending so that within the same
//     importance level the most recent memories come first. Pick as many as
//     fit (measured in characters of rendered text), from the top.
//  3. Sort all picked memories by id ascending so the final order is
//     oldest-first, matching the natural order of events.
//  4. Render the picked memories grouped by date (UTC), each group preceded
//     by a "Weekday, DD Mon YYYY" header and the memories as bullet points,
//     without mentioning importance.
package memories

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zoo/telegram-anthropic-chat/internal/storage"
)

// Select picks memories for the LLM context and renders them grouped by date.
// Memories from the most recent active day are included first (in historical
// order), then older memories by importance (and recency within the same
// importance) for the remaining budget. If no memories fit, an empty string is
// returned.
func Select(memories []storage.Memory, ctxSize int) string {
	picked, _ := Split(memories, ctxSize)
	return Render(picked)
}

// Split runs the selection algorithm and returns two slices: the memories that
// fit under ctxSize (sorted by id ascending for the natural order of events),
// and the remaining memories (also sorted by id ascending for stable display).
func Split(memories []storage.Memory, ctxSize int) (in, out []storage.Memory) {
	if len(memories) == 0 || ctxSize <= 0 {
		return nil, append([]storage.Memory(nil), memories...)
	}

	// Partition into fresh (from the most recent active day) and older
	// (everything else), both in id-ascending (historical) order.
	ordered := make([]storage.Memory, len(memories))
	copy(ordered, memories)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})

	latestDay := latestDayUnix(ordered)

	var fresh, older []storage.Memory
	for _, m := range ordered {
		if dayStartUnix(m.Date) == latestDay {
			fresh = append(fresh, m)
		} else {
			older = append(older, m)
		}
	}

	used := 0
	pickedFresh := make(map[int]bool)
	// 1. Pick all fresh memories that fit, in historical order.
	for _, m := range fresh {
		ln := len(m.Text) + 1
		if used+ln > ctxSize {
			break
		}
		pickedFresh[m.ID] = true
		used += ln
	}

	// 2. Sort older memories by importance desc, then id desc (most recent
	// first within the same importance), and pick as many as fit in the
	// remaining budget.
	sortedOlder := make([]storage.Memory, len(older))
	copy(sortedOlder, older)
	sort.SliceStable(sortedOlder, func(i, j int) bool {
		if sortedOlder[i].Importance != sortedOlder[j].Importance {
			return sortedOlder[i].Importance > sortedOlder[j].Importance
		}
		return sortedOlder[i].ID > sortedOlder[j].ID
	})

	pickedOlderSet := make(map[int]bool, len(sortedOlder))
	for _, m := range sortedOlder {
		ln := len(m.Text) + 1
		if used+ln > ctxSize {
			break
		}
		pickedOlderSet[m.ID] = true
		used += ln
	}

	// 3. Build the in/out slices. Both are sorted by id ascending: the "in"
	// slice so the final order is oldest-first, and the "out" slice for stable
	// display. Iterate over the id-ascending partitions to achieve this.
	for _, m := range older {
		if pickedOlderSet[m.ID] {
			in = append(in, m)
		} else {
			out = append(out, m)
		}
	}
	for _, m := range fresh {
		if pickedFresh[m.ID] {
			in = append(in, m)
		} else {
			out = append(out, m)
		}
	}
	return in, out
}

// Stale returns the memories that are candidates for automatic deletion at the
// end of a session: those that did not fit into the ctxSize budget (i.e. they
// are in the "out" partition of Split) AND are older than maxAge relative to
// now. A memory counts as "older than maxAge" when its Date is strictly before
// now-maxAge, so a memory exactly maxAge old is kept.
//
// Memories that fit the budget are never returned, regardless of age — valid
// in-context memories are never pruned. If maxAge <= 0 the feature is disabled
// and no memories are considered stale.
func Stale(memories []storage.Memory, ctxSize int, maxAge time.Duration, now time.Time) []storage.Memory {
	if maxAge <= 0 || len(memories) == 0 {
		return nil
	}
	_, out := Split(memories, ctxSize)
	threshold := now.Add(-maxAge).Unix()
	var stale []storage.Memory
	for _, m := range out {
		if m.Date < threshold {
			stale = append(stale, m)
		}
	}
	return stale
}

// latestDayUnix returns the UTC start-of-day timestamp of the most recent day
// that any of the given memories was created on. Memories are assumed to be
// non-empty and to carry a non-zero Date (the storage layer backfills a zero
// Date with the start of today on load).
func latestDayUnix(memories []storage.Memory) int64 {
	var latest int64
	for _, m := range memories {
		if d := dayStartUnix(m.Date); d > latest {
			latest = d
		}
	}
	return latest
}

// dayStartUnix truncates a Unix timestamp (seconds) to the start of its UTC
// day and returns that start-of-day timestamp.
func dayStartUnix(ts int64) int64 {
	return time.Unix(ts, 0).UTC().Truncate(24 * time.Hour).Unix()
}

// Render renders a slice of memories grouped by their creation date (UTC).
// Each group is preceded by a "Weekday, DD Mon YYYY" header, followed by the
// memories of that day as bullet points ("- text"). Memories are assumed to be
// sorted by id ascending (oldest first), which matches the natural order of
// events, so memories sharing a day appear consecutively. Importance is not
// mentioned. Returns an empty string for an empty slice.
func Render(memories []storage.Memory) string {
	if len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	var curDay string
	for _, m := range memories {
		day := time.Unix(m.Date, 0).UTC().Format("Monday, 02 Jan 2006")
		if day != curDay {
			if curDay != "" {
				// The previous bullet already ended with '\n'; one more
				// produces a single blank line separating the date groups.
				b.WriteByte('\n')
			}
			b.WriteString(day)
			b.WriteByte('\n')
			curDay = day
		}
		b.WriteString("- ")
		b.WriteString(m.Text)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

// RenderList renders a slice of memories as Markdown intended for user-facing
// displays (e.g. the /mem command). Each memory is shown as a bold id, the
// creation date (YYYY-MM-DD, UTC) in parentheses, and the memory text. Every
// line ends with two trailing spaces, which CommonMark treats as a hard line
// break: this keeps the output compact (one line per memory) while preventing
// consecutive non-blank lines from merging into a single paragraph. The output
// is meant to be sent through the MarkdownV2 converter. Returns an empty string
// for an empty slice.
func RenderList(memories []storage.Memory) string {
	if len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range memories {
		date := time.Unix(m.Date, 0).UTC().Format("2006-01-02")
		fmt.Fprintf(&b, "**#%d** (%s) %s  \n", m.ID, date, m.Text)
	}
	return strings.TrimRight(b.String(), " \n")
}
