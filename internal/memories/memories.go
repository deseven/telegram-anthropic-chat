// Package memories implements memory selection for the chat context.
//
// Selection algorithm:
//  1. Pick all memories belonging to one of the recent sessions (the last N
//     session UUIDs stored on the user), in historical (id ascending) order.
//     These provide fresh context and are always included first.
//  2. For the remaining budget, sort all other memories by importance
//     descending and pick as many as fit (measured in characters of rendered
//     text), from the top.
//  3. Sort the picked older memories by id ascending and prepend them to the
//     fresh-context memories, so the final order is oldest-first.
//  4. Render a plain newline-separated list, without mentioning importance.
package memories

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zoo/telegram-anthropic-chat/internal/storage"
)

// Select picks memories for the LLM context and renders them as a plain list.
// Memories from the recent sessions are included first (in historical order),
// then older memories by importance for the remaining budget. If no memories
// fit, an empty string is returned.
func Select(memories []storage.Memory, ctxSize int, recentSessions []string) string {
	picked, _ := Split(memories, ctxSize, recentSessions)
	return Render(picked)
}

// Split runs the selection algorithm and returns two slices: the memories that
// fit under ctxSize (in the order they are sent to the LLM: older important
// memories first, then fresh-context memories from recent sessions, each group
// by id ascending), and the remaining memories (also sorted by id ascending
// for stable display).
func Split(memories []storage.Memory, ctxSize int, recentSessions []string) (in, out []storage.Memory) {
	if len(memories) == 0 || ctxSize <= 0 {
		return nil, append([]storage.Memory(nil), memories...)
	}

	recent := make(map[string]bool, len(recentSessions))
	for _, s := range recentSessions {
		recent[s] = true
	}

	// Partition into fresh (from a recent session) and older (everything else),
	// both in id-ascending (historical) order.
	ordered := make([]storage.Memory, len(memories))
	copy(ordered, memories)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].ID < ordered[j].ID
	})

	var fresh, older []storage.Memory
	for _, m := range ordered {
		if recent[m.SessionUUID] {
			fresh = append(fresh, m)
		} else {
			older = append(older, m)
		}
	}

	used := 0
	pickedFresh := make(map[int]bool)
	// 1. Pick all fresh-context memories that fit, in historical order.
	for _, m := range fresh {
		ln := len(m.Text) + 1
		if used+ln > ctxSize {
			break
		}
		pickedFresh[m.ID] = true
		used += ln
	}

	// 2. Sort older memories by importance desc (stable to keep historical
	// order on ties) and pick as many as fit in the remaining budget.
	sortedOlder := make([]storage.Memory, len(older))
	copy(sortedOlder, older)
	sort.SliceStable(sortedOlder, func(i, j int) bool {
		return sortedOlder[i].Importance > sortedOlder[j].Importance
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

	// 3. Build the in/out slices. The "in" slice is: picked older memories
	// (id ascending) prepended to picked fresh memories (id ascending).
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

// Render renders a slice of memories as a plain newline-separated list,
// without mentioning importance. Returns an empty string for an empty slice.
func Render(memories []storage.Memory) string {
	if len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	for _, m := range memories {
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
