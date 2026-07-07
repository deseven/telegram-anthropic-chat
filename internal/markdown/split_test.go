package markdown

import (
	"strings"
	"testing"
)

// TestSplitMarkdownFits asserts that every chunk returned by SplitMarkdown, when
// converted to MarkdownV2, fits within the message cap.
func TestSplitMarkdownFits(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{
			name: "short text unchanged",
			in:   "Hello! Price is 5$ + tax.",
		},
		{
			name: "long plain text",
			in:   strings.Repeat("The quick brown fox jumps over the lazy dog. ", 200),
		},
		{
			name: "long single paragraph no newlines",
			in:   strings.Repeat("word ", 2000),
		},
		{
			name: "multiple paragraphs",
			in:   strings.Repeat("Para one line.\n\nPara two line.\n\n", 300),
		},
		{
			name: "code block split across messages",
			in: "```go\n" + strings.Repeat("fmt.Println(\"line\")\n", 400) + "```\n",
		},
		{
			name: "code block with language tag",
			in: "```python\n" + strings.Repeat("print('x')\n", 400) + "```\n",
		},
		{
			name: "mixed text and code",
			in: "Here is some text.\n\n" +
				"```go\n" + strings.Repeat("a()\n", 300) + "```\n\n" +
				"More text after.\n\n" +
				strings.Repeat("Final paragraph line.\n", 200),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := SplitMarkdown(tc.in)
			if len(chunks) == 0 {
				t.Fatalf("SplitMarkdown returned no chunks for non-empty input")
			}
			for i, c := range chunks {
				conv := ToMarkdownV2(c)
				if l := len([]rune(conv)); l > MaxMessageLen {
					t.Errorf("chunk %d converted length %d exceeds %d", i, l, MaxMessageLen)
				}
			}
		})
	}
}

// TestSplitMarkdownCodeFencesBalanced asserts that every chunk's converted text
// has a balanced number of fence delimiters, so no message leaves a code block
// open.
func TestSplitMarkdownCodeFencesBalanced(t *testing.T) {
	in := "```go\n" + strings.Repeat("fmt.Println(\"line\")\n", 500) + "```\n"
	chunks := SplitMarkdown(in)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks for a large code block, got %d", len(chunks))
	}
	for i, c := range chunks {
		conv := ToMarkdownV2(c)
		count := strings.Count(conv, "```")
		if count%2 != 0 {
			t.Errorf("chunk %d has unbalanced code fences (%d occurrences)", i, count)
		}
	}
}

// TestSplitMarkdownPreservesContent asserts that concatenating the converted
// chunks (stripped of the synthetic fence close/reopen markers) roughly
// contains the original code lines.
func TestSplitMarkdownPreservesContent(t *testing.T) {
	line := "fmt.Println(\"line\")"
	in := "```go\n" + strings.Repeat(line+"\n", 100) + "```\n"
	chunks := SplitMarkdown(in)
	joined := strings.Join(chunks, "")
	// Every original code line should appear at least once across the chunks.
	if got := strings.Count(joined, line); got != 100 {
		t.Errorf("expected %d occurrences of code line, got %d", 100, got)
	}
}

// TestSplitMarkdownLimitSmall exercises the hard-split path with a tiny limit.
func TestSplitMarkdownLimitSmall(t *testing.T) {
	in := strings.Repeat("word ", 500)
	chunks := SplitMarkdownLimit(in, 50)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if l := convertedLen(c); l > 50 {
			t.Errorf("chunk %d converted length %d exceeds limit 50", i, l)
		}
	}
}

// TestSplitPlainTextFits asserts that every chunk returned by SplitPlainText is
// within the message cap and that content is preserved (no characters dropped
// except the joining newlines that are re-added when concatenating with "\n").
func TestSplitPlainTextFits(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"short text unchanged", "Hello! Price is 5$ + tax."},
		{"long plain text", strings.Repeat("The quick brown fox jumps over the lazy dog. ", 200)},
		{"long single line no newlines", strings.Repeat("word ", 2000)},
		{"many short lines", strings.Repeat("mem line\n", 800)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunks := SplitPlainText(tc.in)
			if len(chunks) == 0 {
				t.Fatalf("SplitPlainText returned no chunks for non-empty input")
			}
			for i, c := range chunks {
				if l := len([]rune(c)); l > MaxMessageLen {
					t.Errorf("chunk %d length %d exceeds %d", i, l, MaxMessageLen)
				}
			}
			// No actual content characters may be dropped: only newlines are
			// inserted/dropped at chunk boundaries (line-boundary splits drop a
			// joining newline; hard-splits of over-long lines insert one). So
			// stripping newlines from both sides must yield identical text.
			got := strings.ReplaceAll(strings.Join(chunks, "\n"), "\n", "")
			want := strings.ReplaceAll(tc.in, "\n", "")
			if got != want {
				t.Errorf("content mismatch (newlines stripped):\n got len %d\nwant len %d", len(got), len(want))
			}
		})
	}
}

// TestSplitPlainTextEmpty asserts that empty/whitespace-only input yields nil.
func TestSplitPlainTextEmpty(t *testing.T) {
	if got := SplitPlainText(""); got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
	if got := SplitPlainText("   \n  "); got != nil {
		t.Fatalf("expected nil for whitespace-only input, got %v", got)
	}
}
