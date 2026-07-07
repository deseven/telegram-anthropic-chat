package prompt

import (
	"testing"
	"time"
)

func TestRender_NowFormat(t *testing.T) {
	// Reference moment: 2006-01-02 15:04:05 MST. Using it as the input
	// makes the expected output trivially derivable from the layout.
	now := time.Date(2006, time.January, 2, 15, 4, 5, 0, time.UTC)

	got := Render("{now}", "", "", now)
	want := "Monday, 02 Jan 2006 15:04"
	if got != want {
		t.Fatalf("Render({now}) = %q, want %q", got, want)
	}
}

func TestRenderWithHistory_NowFormat(t *testing.T) {
	now := time.Date(2006, time.January, 2, 15, 4, 5, 0, time.UTC)

	got := RenderWithHistory("{now}", "", "", "", now)
	want := "Monday, 02 Jan 2006 15:04"
	if got != want {
		t.Fatalf("RenderWithHistory({now}) = %q, want %q", got, want)
	}
}

// TestRender_NowFormat_NonReferenceYear guards against the original bug where
// the layout used the literal "2026" instead of the Go reference-year token
// "2006". With the buggy layout, a real date in 2026 rendered as "6066".
func TestRender_NowFormat_NonReferenceYear(t *testing.T) {
	now := time.Date(2026, time.July, 6, 15, 9, 0, 0, time.UTC)

	got := Render("{now}", "", "", now)
	want := "Monday, 06 Jul 2026 15:09"
	if got != want {
		t.Fatalf("Render({now}) = %q, want %q (regression: year token)", got, want)
	}
}
