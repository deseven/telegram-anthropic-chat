package llm

import "testing"

func TestStripJSONFences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain json no fences",
			in:   `{"memories":[]}`,
			want: `{"memories":[]}`,
		},
		{
			name: "json fence with json lang tag",
			in:   "```json\n{\"memories\":[]}\n```",
			want: `{"memories":[]}`,
		},
		{
			name: "json fence no lang tag",
			in:   "```\n{\"memories\":[]}\n```",
			want: `{"memories":[]}`,
		},
		{
			name: "fence with surrounding commentary",
			in:   "Sure, here are the memories:\n```json\n{\"memories\":[]}\n```\nHope that helps!",
			want: `{"memories":[]}`,
		},
		{
			name: "fence with leading whitespace and lang tag",
			in:   "  ```json\n  {\"memories\":[]}\n```  ",
			want: `{"memories":[]}`,
		},
		{
			name: "no closing fence returns inner content",
			in:   "```json\n{\"memories\":[]}",
			want: `{"memories":[]}`,
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},
		{
			name: "only whitespace",
			in:   "   \n  ",
			want: "",
		},
		{
			name: "json with nested braces inside fence",
			in:   "```json\n{\"memories\":[{\"importance\":7,\"text\":\"hi\"}]}\n```",
			want: `{"memories":[{"importance":7,"text":"hi"}]}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripJSONFences(tc.in)
			if got != tc.want {
				t.Fatalf("stripJSONFences(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
