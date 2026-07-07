package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadJSONC(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	src := `{
		// required fields
		"apiKey": "k",
		"botToken": "t",
		/* block comment */
		"model": "claude-sonnet-5"
	}`
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if c.APIKey != "k" || c.BotToken != "t" {
		t.Fatalf("unexpected fields: %+v", c)
	}
	if c.Model != "claude-sonnet-5" {
		t.Fatalf("model = %q", c.Model)
	}
	// defaults
	if c.MaxTokens != 16384 {
		t.Fatalf("maxTokens default = %d", c.MaxTokens)
	}
	if c.SessionTimeout != 3600 {
		t.Fatalf("sessionTimeout default = %d", c.SessionTimeout)
	}
	if c.BotUpdateMethod != "polling" {
		t.Fatalf("method default = %q", c.BotUpdateMethod)
	}
}

func TestLoadMissingRequired(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	src := `{
		"botToken": "t" // missing apiKey
	}`
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for missing apiKey")
	}
}

func TestLoadUnknownField(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	src := `{"apiKey":"k","botToken":"t","unknownField":1}`
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}
