// Package markdown converts CommonMark markdown into Telegram MarkdownV2 using
// the goldmark-tgmd library, which is a goldmark extension that renders AST
// nodes directly to Telegram-compatible MarkdownV2 (with correct escaping of
// special characters inside and outside formatting constructs).
package markdown

import (
	"bytes"
	"regexp"
	"strings"

	tgmd "github.com/Mad-Pixels/goldmark-tgmd"
)

// MaxMessageLen is Telegram's hard limit for a single message's text field,
// in UTF-16 code units. We measure in runes, which is a close approximation
// for typical text; safeLimit leaves a margin to stay safely under the cap.
const (
	MaxMessageLen = 4096
	safeLimit     = 4000
)

// ToMarkdownV2 converts CommonMark markdown to Telegram MarkdownV2.
func ToMarkdownV2(s string) string {
	var buf bytes.Buffer
	md := tgmd.TGMD()
	if err := md.Convert([]byte(preprocessOrderedLists(s)), &buf); err != nil {
		// goldmark.Convert only returns an error from a renderer panic hook;
		// in practice it never fails for valid input. If it ever does, return
		// the original text so the caller's plain-text fallback applies.
		return s
	}
	// The zero-width space inserted by preprocessOrderedLists is only needed
	// to keep goldmark from recognising ordered-list markers during rendering.
	// Once conversion is done it has served its purpose, so we strip it: it is
	// invisible to the reader but would otherwise leak into the final Telegram
	// message text (e.g. when a user copies the message).
	return strings.ReplaceAll(buf.String(), zeroWidthSpace, "")
}

// orderedItemRe matches a line that begins an ordered (numbered) list item,
// e.g. "1. " or "12. ".
var orderedItemRe = regexp.MustCompile(`^(\d{1,9})\. `)

// zeroWidthSpace is inserted between the number and the dot of an ordered-list
// marker so goldmark no longer recognises the line as a list. It is invisible
// to the reader but breaks the "digits + dot + space" pattern that CommonMark
// uses to detect ordered lists.
const zeroWidthSpace = "\u200B"

// preprocessOrderedLists neutralises ordered-list markers so that the
// goldmark-tgmd renderer does not mangle them. The library renders every list
// (ordered or not) with bullet characters and places the bullet on its own
// line, which turns:
//
//	1. something
//	2. something else
//
// into:
//
//	  •
//	something
//	  •
//	something else
//
// To avoid this we insert a zero-width space between the number and the dot of
// each ordered-list marker so goldmark no longer parses the lines as a list,
// and we surround each item with blank lines so it forms its own paragraph
// (otherwise adjacent text would merge into the same paragraph). The visible
// result keeps the original "1. text" numbering intact.
func preprocessOrderedLists(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i, line := range lines {
		isItem := orderedItemRe.MatchString(line)
		if isItem {
			line = orderedItemRe.ReplaceAllString(line, "$1"+zeroWidthSpace+". ")
		}
		// Separate the item from a preceding non-blank line.
		if isItem && len(out) > 0 && strings.TrimSpace(out[len(out)-1]) != "" {
			out = append(out, "")
		}
		// Separate the item from a following non-blank line that is not itself
		// an ordered item, so trailing text doesn't merge into the paragraph.
		if isItem && i < len(lines)-1 {
			next := lines[i+1]
			if strings.TrimSpace(next) != "" && !orderedItemRe.MatchString(next) {
				out = append(out, line, "")
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// convertedLen returns the rune length of the MarkdownV2 rendering of s. This
// is used to decide whether a chunk fits within Telegram's message cap.
func convertedLen(s string) int {
	return len([]rune(ToMarkdownV2(s)))
}

// isFenceLine reports whether the line opens or closes a fenced code block
// (``` or ~~~, optionally with a language tag).
func isFenceLine(line string) bool {
	trimmed := strings.TrimLeft(line, " \t")
	return strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")
}

// SplitMarkdown splits CommonMark source text into chunks such that each
// chunk, when converted to Telegram MarkdownV2, fits within Telegram's
// 4096-character message cap.
//
// It prefers to break at line boundaries and never leaves a fenced code block
// open across messages: when a code block must be split, the fence is closed
// at the end of one chunk and reopened (with the original language tag) at the
// start of the next. Pathologically long single lines that still exceed the
// cap on their own are hard-split, preferring word boundaries.
func SplitMarkdown(text string) []string {
	return SplitMarkdownLimit(text, safeLimit)
}

// SplitMarkdownLimit is like SplitMarkdown but lets the caller specify the
// maximum converted length per chunk. It is primarily exposed for testing.
func SplitMarkdownLimit(text string, limit int) []string {
	if limit <= 0 {
		limit = safeLimit
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if convertedLen(text) <= limit {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var buf strings.Builder
	inFence := false
	var fenceOpener string // exact opener line, reused when reopening a split code block

	flush := func() {
		s := buf.String()
		buf.Reset()
		if strings.TrimSpace(s) != "" {
			chunks = append(chunks, s)
		}
	}

	for _, line := range lines {
		opener := isFenceLine(line) && !inFence
		closer := inFence && isFenceLine(line)

		candidate := buf.String() + line + "\n"

		switch {
		case convertedLen(candidate) <= limit:
			buf.Reset()
			buf.WriteString(candidate)
			if opener {
				inFence = true
				fenceOpener = line
			}
			if closer {
				inFence = false
			}

		case buf.Len() == 0:
			// A single line on its own already exceeds the limit: hard-split
			// it so we always make progress.
			parts := splitHard(line, limit)
			for j, p := range parts {
				if j < len(parts)-1 {
					chunks = append(chunks, p+"\n")
				} else {
					buf.WriteString(p + "\n")
				}
			}
			// Fence state is unchanged; a fence opener that is itself longer
			// than the limit is pathological and left as-is.

		case closer:
			// The closing fence is tiny but didn't fit because the buffer is
			// near the limit. Close the fence synthetically, flush, and mark
			// the block closed without emitting a redundant empty block.
			buf.WriteString("```")
			inFence = false
			flush()

		case inFence:
			// Inside a code block that must be split: close the fence at the
			// end of this chunk, flush, then reopen the fence in a new chunk
			// and continue accumulating code lines there.
			buf.WriteString("```")
			flush()
			buf.WriteString(fenceOpener + "\n")
			buf.WriteString(line + "\n")

		default:
			// Normal text: flush the current chunk and start a new one.
			flush()
			buf.WriteString(line + "\n")
		}
	}
	flush()
	return chunks
}

// splitHard breaks a single line into pieces each of which converts to at most
// limit MarkdownV2 runes. It prefers to cut at the last space within the
// fitting prefix so words aren't split mid-token.
func splitHard(line string, limit int) []string {
	if convertedLen(line) <= limit {
		return []string{line}
	}
	var parts []string
	rest := line
	for convertedLen(rest) > limit {
		runes := []rune(rest)
		// Binary search for the largest prefix whose conversion fits.
		lo, hi, best := 1, len(runes), 1
		for lo <= hi {
			mid := (lo + hi) / 2
			if convertedLen(string(runes[:mid])) <= limit {
				best = mid
				lo = mid + 1
			} else {
				hi = mid - 1
			}
		}
		cut := best
		// Prefer a word boundary in the second half of the fitting prefix.
		if cut < len(runes) {
			if sp := strings.LastIndexByte(string(runes[:cut]), ' '); sp > cut/2 {
				cut = sp
			}
		}
		if cut <= 0 {
			cut = 1 // guarantee progress
		}
		parts = append(parts, string(runes[:cut]))
		rest = string(runes[cut:])
	}
	parts = append(parts, rest)
	return parts
}

// SplitPlainText splits plain (unparsed) text into chunks each no longer than
// limit runes, preferring to break at line boundaries. Single lines that exceed
// the limit on their own are hard-split, preferring word boundaries. It is the
// plain-text counterpart to SplitMarkdown, for messages sent without MarkdownV2
// parsing (e.g. command replies such as /mem) that may still exceed Telegram's
// 4096-character message cap.
//
// An empty or whitespace-only input returns nil.
func SplitPlainText(text string) []string {
	return splitPlainText(text, MaxMessageLen)
}

func splitPlainText(text string, limit int) []string {
	if limit <= 0 {
		limit = MaxMessageLen
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if len([]rune(text)) <= limit {
		return []string{text}
	}

	lines := strings.Split(text, "\n")
	var chunks []string
	var buf strings.Builder

	flush := func() {
		s := buf.String()
		buf.Reset()
		if s != "" {
			chunks = append(chunks, s)
		}
	}

	for _, line := range lines {
		// A line plus the joining newline must fit; if the line alone exceeds
		// the limit, hard-split it so we always make progress.
		if len([]rune(line)) > limit {
			flush()
			for _, p := range splitHard(line, limit) {
				chunks = append(chunks, p)
			}
			continue
		}
		candidate := buf.String()
		if buf.Len() > 0 {
			candidate += "\n"
		}
		candidate += line
		if len([]rune(candidate)) > limit {
			flush()
			buf.WriteString(line)
		} else {
			buf.Reset()
			buf.WriteString(candidate)
		}
	}
	flush()
	return chunks
}
