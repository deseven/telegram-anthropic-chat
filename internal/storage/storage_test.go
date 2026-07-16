package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeleteMemory(t *testing.T) {
	ud := &UserData{
		UserDescription: "desc",
		Memories: []Memory{
			{ID: 1, Importance: 5, Text: "one"},
			{ID: 2, Importance: 9, Text: "two"},
			{ID: 3, Importance: 3, Text: "three"},
		},
	}

	if !ud.DeleteMemory(2) {
		t.Fatal("expected DeleteMemory(2) to return true")
	}
	if len(ud.Memories) != 2 {
		t.Fatalf("expected 2 memories left, got %d", len(ud.Memories))
	}
	// Remaining ids are stable (not renumbered): 1 and 3.
	if ud.Memories[0].ID != 1 || ud.Memories[1].ID != 3 {
		t.Fatalf("unexpected remaining ids: %+v", ud.Memories)
	}

	// Deleting a non-existent id is a no-op returning false.
	if ud.DeleteMemory(99) {
		t.Fatal("expected DeleteMemory(99) to return false")
	}
	if len(ud.Memories) != 2 {
		t.Fatalf("expected 2 memories still, got %d", len(ud.Memories))
	}

	// Deleting the first element works.
	if !ud.DeleteMemory(1) {
		t.Fatal("expected DeleteMemory(1) to return true")
	}
	if len(ud.Memories) != 1 || ud.Memories[0].ID != 3 {
		t.Fatalf("expected only id 3 left, got %+v", ud.Memories)
	}

	// Deleting the last element works.
	if !ud.DeleteMemory(3) {
		t.Fatal("expected DeleteMemory(3) to return true")
	}
	if len(ud.Memories) != 0 {
		t.Fatalf("expected no memories left, got %d", len(ud.Memories))
	}

	// Deleting from an empty list is a no-op.
	if ud.DeleteMemory(1) {
		t.Fatal("expected DeleteMemory on empty list to return false")
	}
}

// TestDeleteMemories verifies batch deletion by id set, including that
// non-existent ids are ignored and that ids of remaining memories are stable.
func TestDeleteMemories(t *testing.T) {
	ud := &UserData{
		UserDescription: "desc",
		Memories: []Memory{
			{ID: 1, Text: "one"},
			{ID: 2, Text: "two"},
			{ID: 3, Text: "three"},
			{ID: 4, Text: "four"},
		},
	}
	n := ud.DeleteMemories(map[int]bool{2: true, 4: true, 99: true})
	if n != 2 {
		t.Fatalf("expected 2 deleted, got %d", n)
	}
	if len(ud.Memories) != 2 {
		t.Fatalf("expected 2 remaining, got %d: %+v", len(ud.Memories), ud.Memories)
	}
	want := []int{1, 3}
	for i, w := range want {
		if ud.Memories[i].ID != w {
			t.Fatalf("Memories[%d].ID = %d, want %d (full: %+v)", i, ud.Memories[i].ID, w, ud.Memories)
		}
	}

	// An empty (or nil) id set is a no-op.
	if n := ud.DeleteMemories(nil); n != 0 {
		t.Fatalf("expected 0 deleted for nil ids, got %d", n)
	}
	if len(ud.Memories) != 2 {
		t.Fatalf("nil ids must not change memories, got %d", len(ud.Memories))
	}

	// Deleting all remaining empties the slice.
	if n := ud.DeleteMemories(map[int]bool{1: true, 3: true}); n != 2 {
		t.Fatalf("expected 2 deleted, got %d", n)
	}
	if len(ud.Memories) != 0 {
		t.Fatalf("expected empty after deleting all, got %d: %+v", len(ud.Memories), ud.Memories)
	}
}

func TestNextMemoryIDAfterDelete(t *testing.T) {
	// NextMemoryID is max(remaining)+1. Deleting a non-max memory leaves the
	// max unchanged, so the next id is unaffected.
	ud := &UserData{
		UserDescription: "desc",
		Memories: []Memory{
			{ID: 1, Text: "one"},
			{ID: 5, Text: "five"},
		},
	}
	ud.DeleteMemory(1)
	if got := ud.NextMemoryID(); got != 6 {
		t.Fatalf("NextMemoryID = %d, want 6 (max unchanged)", got)
	}
}

// TestAddMemoriesStampsDate asserts that AddMemories stamps every new memory
// with a non-zero creation timestamp (and leaves an explicit date untouched).
func TestAddMemoriesStampsDate(t *testing.T) {
	ud := &UserData{UserDescription: "desc"}
	in := []Memory{
		{Importance: 5, Text: "auto date"},
		{Importance: 3, Text: "explicit date", Date: 1000},
	}
	ud.AddMemories(in)

	if ud.Memories[0].Date == 0 {
		t.Fatal("expected auto-stamped date to be non-zero")
	}
	// The explicit date must be preserved, not overwritten.
	if ud.Memories[1].Date != 1000 {
		t.Fatalf("expected explicit date 1000 to be preserved, got %d", ud.Memories[1].Date)
	}
	// The stamped date should be recent (within the last minute).
	now := time.Now().UTC().Unix()
	if d := ud.Memories[0].Date; d < now-60 || d > now+60 {
		t.Fatalf("stamped date %d not within ~now (%d)", d, now)
	}
}

// TestLoadBackfillsMissingDate asserts that memories loaded from disk without a
// date are backfilled with the start of today (UTC), while memories that already
// carry a date keep it.
func TestLoadBackfillsMissingDate(t *testing.T) {
	dir := t.TempDir()
	s, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A data file with two memories: one without a date, one with an explicit
	// date. This mirrors the pre-date on-disk format for the first memory.
	raw := `{
		"user_description": "desc",
		"memories": [
			{"id": 1, "importance": 5, "text": "no date"},
			{"id": 2, "importance": 3, "text": "has date", "date": 1000}
		]
	}`
	if err := os.WriteFile(filepath.Join(dir, "1.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	ud, err := s.Load(1)
	if err != nil {
		t.Fatal(err)
	}
	// The memory without a date is backfilled to the start of today.
	startOfToday := time.Now().UTC().Truncate(24 * time.Hour).Unix()
	if ud.Memories[0].Date != startOfToday {
		t.Fatalf("missing date backfilled to %d, want %d (start of today)", ud.Memories[0].Date, startOfToday)
	}
	// The explicit date is preserved.
	if ud.Memories[1].Date != 1000 {
		t.Fatalf("explicit date changed to %d, want 1000", ud.Memories[1].Date)
	}
}
