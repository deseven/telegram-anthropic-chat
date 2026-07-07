package storage

import "testing"

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
