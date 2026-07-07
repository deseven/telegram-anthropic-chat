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
			want: "\n1\u200B\\. something\n\n2\u200B\\. something else\n",
		},
		{
			name: "ordered list three items",
			in:   "1. first\n2. second\n3. third",
			want: "\n1\u200B\\. first\n\n2\u200B\\. second\n\n3\u200B\\. third\n",
		},
		{
			name: "ordered list separated from text",
			in:   "intro\n1. one\n2. two\noutro",
			want: "\nintro\n\n1\u200B\\. one\n\n2\u200B\\. two\n\noutro\n",
		},
		{
			name: "unordered list still bulleted",
			in:   "- a\n- b\n- c",
			want: "\n  • a\n  • b\n  • c\n",
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
