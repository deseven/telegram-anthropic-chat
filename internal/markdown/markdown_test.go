package markdown

import "testing"

// These tests assert the exact output of goldmark-tgmd, including the
// leading/trailing newlines the library emits for block-level formatting.
// They document the current rendering so changes are visible on review.

func TestToMarkdownV2(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain text escapes special chars",
			in:   "Hello! Price is 5$ + tax.",
			want: "\nHello\\! Price is 5$ \\+ tax\\.\n",
		},
		{
			name: "bold",
			in:   "This is **bold** text",
			want: "\nThis is ***bold*** text\n",
		},
		{
			name: "italic",
			in:   "This is *italic* text",
			want: "\nThis is _italic_ text\n",
		},
		{
			name: "strikethrough",
			in:   "This is ~~struck~~ text",
			want: "\nThis is ~~~struck~~~ text\n",
		},
		{
			name: "blockquote",
			in:   "> quoted text",
			want: "\n>quoted text\n",
		},
		{
			name: "inline code",
			in:   "Use `code` here",
			want: "\nUse `code` here\n",
		},
		{
			name: "fenced code block",
			in:   "```\ncode line\n```",
			want: "\n```\ncode line\n```\n",
		},
		{
			name: "fenced code block with language tag",
			in:   "```go\nfmt.Println()\n```",
			want: "\n```go\nfmt.Println()\n```\n",
		},
		{
			name: "fenced code block with python tag",
			in:   "```python\ndef test(x):\n  something\n```",
			want: "\n```python\ndef test(x):\n  something\n```\n",
		},
		{
			name: "link",
			in:   "See [docs](https://example.com).",
			want: "\nSee [docs](https://example.com)\\.\n",
		},
		{
			name: "stray asterisk not italic",
			in:   "2 * 3 = 6",
			want: "\n2 \\* 3 \\= 6\n",
		},
		{
			name: "underscore escaped",
			in:   "snake_case_var",
			want: "\nsnake\\_case\\_var\n",
		},
		{
			name: "mixed bold and italic",
			in:   "**bold** and *italic*",
			want: "\n***bold*** and _italic_\n",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
		{
			name: "heading h1",
			in:   "# Title",
			want: "***\\# Title***",
		},
		{
			name: "heading h2",
			in:   "## Subtitle",
			want: "\n***Subtitle***",
		},
		{
			name: "ordered list keeps numbering",
			in:   "1. something\n2. something else",
			want: "\n  1\\. something\n  2\\. something else\n",
		},
		{
			name: "ordered list three items",
			in:   "1. first\n2. second\n3. third",
			want: "\n  1\\. first\n  2\\. second\n  3\\. third\n",
		},
		{
			name: "ordered list separated from text",
			in:   "intro\n1. one\n2. two\noutro",
			// "outro" is a lazy continuation of item 2, so it aligns under "two".
			want: "\nintro\n\n  1\\. one\n  2\\. two\n     outro\n",
		},
		{
			name: "unordered list still bulleted",
			in:   "- a\n- b\n- c",
			want: "\n  • a\n  • b\n  • c\n",
		},
		{
			name: "soft line break rendered as newline",
			in:   "line one\nline two",
			want: "\nline one\nline two\n",
		},
		{
			name: "ordered list with continuation line",
			in:   "1. item\n   continuation\n2. next",
			// "continuation" aligns under "item" (5 spaces: 2 indent + "1. ").
			want: "\n  1\\. item\n     continuation\n  2\\. next\n",
		},
		{
			name: "nested list with continuation lines keeps indentation",
			in: "1. Something\n" +
				"   Some line that belongs to first point\n" +
				"   Some other line\n" +
				"2. Something else\n" +
				"   Also a continuation line here\n" +
				"   - And even a nested bullet\n" +
				"     with its own wrapped continuation\n" +
				"3. Final point\n" +
				"   Single continuation line",
			want: "\n  1\\. Something\n" +
				"     Some line that belongs to first point\n" +
				"     Some other line\n" +
				"  2\\. Something else\n" +
				"     Also a continuation line here\n" +
				"    ‣ And even a nested bullet\n" +
				"      with its own wrapped continuation\n" +
				"  3\\. Final point\n" +
				"     Single continuation line\n",
		},
		{
			// A "loose" list (blank lines between items) wraps each item's
			// content in a Paragraph node. The marker must stay on the same
			// line as the content (no spurious newline after "1.").
			name: "loose ordered list with bold keeps marker on content line",
			in:   "Три варианта:\n\n1. **mod-llm-chatter** — модуль.\n\n2. **mod-playerbots** — боты.",
			want: "\nТри варианта:\n\n  1\\. ***mod\\-llm\\-chatter*** — модуль\\.\n\n  2\\. ***mod\\-playerbots*** — боты\\.\n\n",
		},
		{
			// Loose unordered list: bullet stays on the same line as content.
			name: "loose unordered list keeps bullet on content line",
			in:   "- **first** item\n\n- **second** item",
			want: "\n  • ***first*** item\n\n  • ***second*** item\n\n",
		},
		{
			// A list item with multiple paragraphs: the second paragraph
			// starts on a new line aligned under the item's text.
			name: "loose list item with multiple paragraphs",
			in:   "1. first para\n\n   second para\n\n2. next",
			want: "\n  1\\. first para\n\n     second para\n\n  2\\. next\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToMarkdownV2(tt.in)
			if got != tt.want {
				t.Errorf("ToMarkdownV2(%q) =\n  %q\nwant\n  %q", tt.in, got, tt.want)
			}
		})
	}
}
