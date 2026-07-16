// Package config loads and validates the application configuration.
//
// The config file is JSONC: standard JSON with `//` line comments and
// `/* ... */` block comments, which are stripped before parsing.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Config holds all application settings.
type Config struct {
	APIKey             string `json:"apiKey"`
	BotToken           string `json:"botToken"`
	BotUpdateMethod    string `json:"botUpdateMethod"`
	Model              string `json:"model"`
	MaxTokens          int    `json:"maxTokens"`
	MemoriesCtxSize    int    `json:"memoriesCtxSize"`
	MemoriesMaxAge     int    `json:"memoriesMaxAge"`
	SessionTimeout     int    `json:"sessionTimeout"`
	SystemPrompt       string `json:"systemPrompt"`
	MemoriesPrompt     string `json:"memoriesPrompt"`
	MemoriesUserPrompt string `json:"memoriesUserPrompt"`
	WebhookPort        int    `json:"webhookPort"`
	WebhookSecretToken string `json:"webhookSecretToken"`
	WebhookPublicURL   string `json:"webhookPublicURL"`
	DumpRequestsPath   string `json:"dumpRequestsPath"`
	TavilyAPIKey       string `json:"tavilyApiKey"`
}

// Load reads the JSONC config file at path, applies defaults and validates
// required fields. It returns an error if required fields are missing.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	cleaned := stripComments(string(raw))

	var c Config
	dec := json.NewDecoder(strings.NewReader(cleaned))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.BotUpdateMethod == "" {
		c.BotUpdateMethod = "polling"
	}
	if c.Model == "" {
		c.Model = "claude-sonnet-5"
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 16384
	}
	if c.MemoriesCtxSize == 0 {
		c.MemoriesCtxSize = 16384
	}
	if c.MemoriesMaxAge == 0 {
		c.MemoriesMaxAge = 30 * 24 * 3600 // 30 days
	}
	if c.SessionTimeout == 0 {
		c.SessionTimeout = 3600
	}
	if c.SystemPrompt == "" {
		c.SystemPrompt = "prompts/system.md"
	}
	if c.MemoriesPrompt == "" {
		c.MemoriesPrompt = "prompts/memories-system.md"
	}
	if c.MemoriesUserPrompt == "" {
		c.MemoriesUserPrompt = "prompts/memories-user.md"
	}
	if c.WebhookPort == 0 {
		c.WebhookPort = 5666
	}
}

func (c *Config) validate() error {
	var missing []string
	if c.APIKey == "" {
		missing = append(missing, "apiKey")
	}
	if c.BotToken == "" {
		missing = append(missing, "botToken")
	}
	if c.BotUpdateMethod != "polling" && c.BotUpdateMethod != "webhook" {
		return fmt.Errorf("botUpdateMethod must be 'polling' or 'webhook', got %q", c.BotUpdateMethod)
	}
	if c.SessionTimeout <= 0 {
		return fmt.Errorf("sessionTimeout must be positive, got %d", c.SessionTimeout)
	}
	if c.MemoriesCtxSize <= 0 {
		return fmt.Errorf("memoriesCtxSize must be positive, got %d", c.MemoriesCtxSize)
	}
	if c.MemoriesMaxAge <= 0 {
		return fmt.Errorf("memoriesMaxAge must be positive, got %d", c.MemoriesMaxAge)
	}
	if c.MaxTokens <= 0 {
		return fmt.Errorf("maxTokens must be positive, got %d", c.MaxTokens)
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config fields: %s", strings.Join(missing, ", "))
	}
	return nil
}

// stripComments removes `//` line comments and `/* ... */` block comments
// from a JSONC source string. It is a simple, string-based preprocessor that
// tracks double-quoted string literals so that `//` or `/*` sequences appearing
// inside string values (e.g. in URLs like "https://...") are preserved.
func stripComments(src string) string {
	var b strings.Builder
	i := 0
	n := len(src)
	inString := false
	for i < n {
		ch := src[i]

		// Inside a string literal: copy verbatim until the closing quote,
		// honoring backslash escapes.
		if inString {
			b.WriteByte(ch)
			if ch == '\\' && i+1 < n {
				b.WriteByte(src[i+1])
				i += 2
				continue
			}
			if ch == '"' {
				inString = false
			}
			i++
			continue
		}

		// Not in a string: a double quote opens one.
		if ch == '"' {
			inString = true
			b.WriteByte(ch)
			i++
			continue
		}

		// Block comment
		if i+1 < n && ch == '/' && src[i+1] == '*' {
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				break
			}
			i += 2 + end + 2
			continue
		}
		// Line comment
		if i+1 < n && ch == '/' && src[i+1] == '/' {
			end := strings.IndexByte(src[i:], '\n')
			if end < 0 {
				break
			}
			i += end
			continue
		}
		b.WriteByte(ch)
		i++
	}
	return b.String()
}

