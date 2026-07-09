package llm

import (
	"encoding/json"
	"testing"
)

func TestSerializeHistory(t *testing.T) {
	msgs := []Message{
		{Role: "user", Blocks: []ContentBlock{{Text: "Hello!"}}},
		{Role: "assistant", Blocks: []ContentBlock{{Text: "Hello! How can I help you?"}}},
	}

	got := SerializeHistory(msgs)

	var entries []historyEntry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, got)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	if entries[0].Author != "user" || entries[0].Message != "Hello!" {
		t.Errorf("entries[0] = {%q, %q}, want {user, Hello!}", entries[0].Author, entries[0].Message)
	}
	if entries[1].Author != "you" || entries[1].Message != "Hello! How can I help you?" {
		t.Errorf("entries[1] = {%q, %q}, want {you, Hello! How can I help you?}", entries[1].Author, entries[1].Message)
	}
}

func TestSerializeHistory_ToolTurns(t *testing.T) {
	msgs := []Message{
		{Role: "user", Blocks: []ContentBlock{{Text: "What's the weather?"}}},
		{Role: "assistant", Blocks: []ContentBlock{
			{ToolUseID: "tu1", ToolUseName: "search", ToolUseInput: json.RawMessage(`{"q":"weather"}`)},
		}},
		{Role: "user", Blocks: []ContentBlock{{ToolResultID: "tu1", ToolResultText: "sunny"}}},
		{Role: "assistant", Blocks: []ContentBlock{{Text: "It's sunny."}}},
	}

	got := SerializeHistory(msgs)

	var entries []historyEntry
	if err := json.Unmarshal([]byte(got), &entries); err != nil {
		t.Fatalf("unmarshal: %v\nraw: %s", err, got)
	}
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}

	if entries[1].Author != "you" {
		t.Errorf("tool_use turn author = %q, want you", entries[1].Author)
	}
	if entries[1].Message != "[tool call: search({\"q\":\"weather\"})]" {
		t.Errorf("tool_use message = %q", entries[1].Message)
	}
	if entries[2].Author != "user" {
		t.Errorf("tool_result turn author = %q, want user", entries[2].Author)
	}
	if entries[2].Message != "[tool call completed]" {
		t.Errorf("tool_result message = %q", entries[2].Message)
	}
}

func TestSerializeHistory_Empty(t *testing.T) {
	if got := SerializeHistory(nil); got != "[]" {
		t.Fatalf("SerializeHistory(nil) = %q, want []", got)
	}
}
