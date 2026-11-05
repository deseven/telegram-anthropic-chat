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
	HTTPPort           int    `json:"httpPort"`
	HTTPPublicURL      string `json:"httpPublicURL"`
	WebhookSecretToken string `json:"webhookSecretToken"`
	DumpRequestsPath   string `json:"dumpRequestsPath"`
	TavilyAPIKey       string `json:"tavilyApiKey"`

	// ImageGeneration enables the generate_image tool. Nil (the default)
	// disables image generation entirely.
	ImageGeneration *ImageGenerationConfig `json:"imageGeneration"`
}

// ImageGenerationConfig configures the OpenAI-compatible image generation
// provider (POST {apiEndpoint}/v1/images/generations).
type ImageGenerationConfig struct {
	// APIKey is sent as a Bearer token when non-empty.
	APIKey string `json:"apiKey"`
	// APIEndpoint is the provider base URL, e.g. "https://api.openai.com"
	// or "http://host:port/api"; "/v1/images/generations" is appended.
	APIEndpoint string `json:"apiEndpoint"`
	Model       string `json:"model"`
	// Timeout is the per-request timeout in seconds (default 60).
	Timeout int `json:"timeout"`
	// DefaultWidth/DefaultHeight are used when the model omits a dimension
	// (default 512).
	DefaultWidth  int `json:"defaultWidth"`
	DefaultHeight int `json:"defaultHeight"`
	// MaxWidth/MaxHeight cap the dimensions the model may request (default 1024).
	MaxWidth  int `json:"maxWidth"`
	MaxHeight int `json:"maxHeight"`
	// ResponseFormat, if set, is sent as response_format ("b64_json" or
	// "url"). Omitted by default since some models reject the parameter.
	ResponseFormat string `json:"responseFormat"`
	// ExtraParams are merged into the request body (e.g. quality, style).
	// The core fields (model, prompt, n, size) always take precedence.
	ExtraParams map[string]any `json:"extraParams"`
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
		// Retention unit per importance point: an out-of-context memory is
		// pruned once it hasn't reached the context for importance*10 days.
		c.MemoriesMaxAge = 10 * 24 * 3600 // 10 days
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
	if c.HTTPPort == 0 {
		c.HTTPPort = 5666
	}
	// Normalize the public URL so handlers can append paths without
	// worrying about a trailing slash.
	c.HTTPPublicURL = strings.TrimRight(c.HTTPPublicURL, "/")

	if ig := c.ImageGeneration; ig != nil {
		ig.APIEndpoint = strings.TrimRight(strings.TrimSpace(ig.APIEndpoint), "/")
		if ig.Timeout == 0 {
			ig.Timeout = 60
		}
		if ig.DefaultWidth == 0 {
			ig.DefaultWidth = 512
		}
		if ig.DefaultHeight == 0 {
			ig.DefaultHeight = 512
		}
		if ig.MaxWidth == 0 {
			ig.MaxWidth = 1024
		}
		if ig.MaxHeight == 0 {
			ig.MaxHeight = 1024
		}
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
	if c.BotUpdateMethod == "webhook" && c.HTTPPublicURL == "" {
		return fmt.Errorf("httpPublicURL is required when botUpdateMethod is 'webhook'")
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
	if ig := c.ImageGeneration; ig != nil {
		if ig.APIEndpoint == "" {
			return fmt.Errorf("imageGeneration.apiEndpoint is required")
		}
		if ig.Model == "" {
			return fmt.Errorf("imageGeneration.model is required")
		}
		if ig.Timeout < 0 || ig.DefaultWidth < 0 || ig.DefaultHeight < 0 || ig.MaxWidth < 0 || ig.MaxHeight < 0 {
			return fmt.Errorf("imageGeneration timeout and sizes must be positive")
		}
		if ig.DefaultWidth > ig.MaxWidth || ig.DefaultHeight > ig.MaxHeight {
			return fmt.Errorf("imageGeneration default size %dx%d exceeds max size %dx%d", ig.DefaultWidth, ig.DefaultHeight, ig.MaxWidth, ig.MaxHeight)
		}
		if ig.ResponseFormat != "" && ig.ResponseFormat != "b64_json" && ig.ResponseFormat != "url" {
			return fmt.Errorf("imageGeneration.responseFormat must be 'b64_json' or 'url', got %q", ig.ResponseFormat)
		}
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
