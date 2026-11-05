// Package imagegen implements an OpenAI-compatible image generation client
// (POST {apiEndpoint}/v1/images/generations) and exposes it to the model as
// the generate_image tool.
package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/zoo/telegram-anthropic-chat/internal/config"
	"github.com/zoo/telegram-anthropic-chat/internal/images"
	"github.com/zoo/telegram-anthropic-chat/internal/llm"
	"github.com/zoo/telegram-anthropic-chat/internal/log"
)

// ToolName is the name of the tool exposed to the model.
const ToolName = "generate_image"

// maxResponseBytes bounds how much of a provider response we read.
const maxResponseBytes = 64 << 20

// Client talks to the image generation provider and implements
// llm.ToolExecutor.
type Client struct {
	cfg   config.ImageGenerationConfig
	http  *http.Client // no client-level timeout; Generate uses a context deadline
	tools []anthropic.ToolUnionParam
}

// New creates a Client from the config. It returns nil when cfg is nil;
// callers should check for nil before using the client.
func New(cfg *config.ImageGenerationConfig) *Client {
	if cfg == nil {
		return nil
	}
	c := &Client{cfg: *cfg, http: &http.Client{}}
	c.tools = c.buildTools()
	return c
}

// Tools returns the tool definition to send with a Messages request.
func (c *Client) Tools() []anthropic.ToolUnionParam { return c.tools }

type toolInput struct {
	Prompt string   `json:"prompt"`
	Width  *float64 `json:"width"`
	Height *float64 `json:"height"`
}

// Execute runs the generate_image tool. Every failure is returned as an error
// result so the model can see it and react (retry, adjust the size, explain
// to the user). On success the original image is handed back for immediate
// delivery to the user, and a JPEG copy (resized like user photos) is
// returned to the model as the tool result.
func (c *Client) Execute(ctx context.Context, name string, input json.RawMessage) llm.ToolResult {
	if name != ToolName {
		return errResult("unknown tool: %s", name)
	}
	var in toolInput
	if err := json.Unmarshal(input, &in); err != nil {
		return errResult("invalid input: %v", err)
	}
	if strings.TrimSpace(in.Prompt) == "" {
		return errResult("prompt is required")
	}
	w, err := dimension("width", in.Width, c.cfg.DefaultWidth, c.cfg.MaxWidth)
	if err != nil {
		return errResult("%v", err)
	}
	h, err := dimension("height", in.Height, c.cfg.DefaultHeight, c.cfg.MaxHeight)
	if err != nil {
		return errResult("%v", err)
	}

	log.Print("imagegen", "generate_image %dx%d: %s", w, h, truncate(in.Prompt, 200))
	start := time.Now()
	raw, err := c.Generate(ctx, in.Prompt, w, h)
	if err != nil {
		log.Print("imagegen", "generate_image failed: %v", err)
		return errResult("image generation failed: %v", err)
	}

	// The model gets the same processing as photos sent by the user
	// (downscaled to 1024px, JPEG 85%).
	b64, err := images.Process(raw)
	if err != nil {
		log.Print("imagegen", "provider returned an unusable image: %v", err)
		return errResult("image generation failed: the provider returned an unusable image: %v", err)
	}
	size := ""
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil {
		size = fmt.Sprintf(" (%dx%d)", cfg.Width, cfg.Height)
	}
	log.Print("imagegen", "generated image%s, %d bytes in %s", size, len(raw), time.Since(start).Round(time.Millisecond))

	// The tool result carries only the image, no text.
	return llm.ToolResult{
		ImageB64: b64,
		Image:    raw,
	}
}

func errResult(format string, args ...any) llm.ToolResult {
	return llm.ToolResult{Text: "ERROR: " + fmt.Sprintf(format, args...), IsError: true}
}

// dimension validates an optional pixel dimension. Only our own limits are
// checked here; anything the provider does not support is reported by the
// provider itself and passed on to the model.
func dimension(name string, v *float64, def, max int) (int, error) {
	if v == nil {
		return def, nil
	}
	if *v != float64(int(*v)) {
		return 0, fmt.Errorf("%s must be an integer number of pixels, got %v", name, *v)
	}
	n := int(*v)
	if n < 1 || n > max {
		return 0, fmt.Errorf("%s must be between 1 and %d pixels, got %d", name, max, n)
	}
	return n, nil
}

type generationResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
		URL     string `json:"url"`
	} `json:"data"`
	Error json.RawMessage `json:"error"`
}

// Generate requests a single image of the given size and returns its raw
// bytes (whatever format the provider produced). The configured timeout
// covers the whole operation, including downloading the image if the
// provider answers with a URL.
func (c *Client) Generate(ctx context.Context, prompt string, width, height int) ([]byte, error) {
	timeout := time.Duration(c.cfg.Timeout) * time.Second
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	body := make(map[string]any, len(c.cfg.ExtraParams)+5)
	for k, v := range c.cfg.ExtraParams {
		body[k] = v
	}
	body["model"] = c.cfg.Model
	body["prompt"] = prompt
	body["n"] = 1
	body["size"] = fmt.Sprintf("%dx%d", width, height)
	if c.cfg.ResponseFormat != "" {
		body["response_format"] = c.cfg.ResponseFormat
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIEndpoint+"/v1/images/generations", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.wrapErr(ctx, "request", err)
	}
	defer resp.Body.Close()
	data, err := readLimited(resp.Body)
	if err != nil {
		return nil, c.wrapErr(ctx, "read response", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("provider returned HTTP %d: %s", resp.StatusCode, errorMessage(data))
	}

	var out generationResponse
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("invalid provider response: %w (body: %s)", err, truncate(string(data), 200))
	}
	if msg := rawErrorMessage(out.Error); msg != "" {
		return nil, fmt.Errorf("provider returned an error: %s", msg)
	}
	if len(out.Data) == 0 {
		return nil, errors.New("provider returned no images")
	}

	item := out.Data[0]
	switch {
	case item.B64JSON != "":
		b64 := item.B64JSON
		if i := strings.Index(b64, ";base64,"); strings.HasPrefix(b64, "data:") && i >= 0 {
			b64 = b64[i+len(";base64,"):]
		}
		raw, err := base64.StdEncoding.DecodeString(b64)
		if err != nil {
			return nil, fmt.Errorf("provider returned invalid base64 image data: %w", err)
		}
		return raw, nil
	case item.URL != "":
		return c.download(ctx, item.URL)
	default:
		return nil, errors.New("provider returned an image entry with neither b64_json nor url")
	}
}

func (c *Client) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build image download request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, c.wrapErr(ctx, "download image", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download image: HTTP %d", resp.StatusCode)
	}
	raw, err := readLimited(resp.Body)
	if err != nil {
		return nil, c.wrapErr(ctx, "download image", err)
	}
	return raw, nil
}

// wrapErr turns context errors into clear messages for the model.
func (c *Client) wrapErr(ctx context.Context, what string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %ds", c.cfg.Timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return errors.New("cancelled")
	}
	return fmt.Errorf("%s: %w", what, err)
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxResponseBytes {
		return nil, fmt.Errorf("response exceeds %d MB", maxResponseBytes>>20)
	}
	return data, nil
}

// errorMessage extracts a readable message from an error response body:
// {"error":{"message":"..."}}, {"error":"..."} or, failing that, the raw body.
func errorMessage(body []byte) string {
	var env struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(body, &env) == nil {
		if msg := rawErrorMessage(env.Error); msg != "" {
			return msg
		}
		if env.Message != "" {
			return env.Message
		}
	}
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		return "(empty body)"
	}
	return truncate(msg, 500)
}

func rawErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return truncate(s, 500)
	}
	var obj struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
		if obj.Code != "" {
			return truncate(obj.Code+": "+obj.Message, 500)
		}
		return truncate(obj.Message, 500)
	}
	return truncate(string(raw), 500)
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// buildTools returns the Anthropic tool definition for generate_image.
func (c *Client) buildTools() []anthropic.ToolUnionParam {
	desc := fmt.Sprintf(`Generate an image from a text prompt and send it straight to the user.

How it works:
- The image is posted to the user immediately as a message from you, and the same image is returned to you as the tool result so you can look at it. Afterwards, reply as usual (e.g. a short comment); the user already has the image, so do not try to send or describe it in detail again.
- Generation takes several seconds, so don't call this tool needlessly. If the result is clearly off, you may generate another image with an improved prompt.
- If generation fails (provider error, unsupported resolution, timeout), you get the error back as the tool result: adjust the parameters and retry, or explain the problem to the user.

Writing a good prompt (English is recommended because most image models understand it best):
- Describe a single, concrete scene in several sentences or comma-separated phrases.
- Subject: who/what is depicted, appearance, pose, action, expression.
- Setting: location, background, time of day, weather.
- Style or medium: photo, oil painting, watercolor, pencil sketch, flat vector illustration, anime, 3D render, pixel art, etc.
- Composition and camera: close-up, wide shot, low angle, top-down, depth of field, lens.
- Lighting, mood and color palette: golden hour, soft studio light, neon, moody, pastel, etc.
- Say what should be in the image rather than what should not.
- Expand on the user's request with sensible details, but do not contradict what they asked for.

Size: width and height are in pixels, optional, default %dx%d, maximum %dx%d. Keep the default unless the user wants a particular shape (e.g. wide or tall).`,
		c.cfg.DefaultWidth, c.cfg.DefaultHeight, c.cfg.MaxWidth, c.cfg.MaxHeight)

	tool := anthropic.ToolParam{
		Name:        ToolName,
		Description: anthropic.String(desc),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "The image prompt: a detailed description of the desired image (subject, setting, style, composition, lighting). English recommended.",
				},
				"width": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     c.cfg.MaxWidth,
					"description": fmt.Sprintf("Image width in pixels. Default %d.", c.cfg.DefaultWidth),
				},
				"height": map[string]any{
					"type":        "integer",
					"minimum":     1,
					"maximum":     c.cfg.MaxHeight,
					"description": fmt.Sprintf("Image height in pixels. Default %d.", c.cfg.DefaultHeight),
				},
			},
			Required: []string{"prompt"},
		},
	}
	return []anthropic.ToolUnionParam{{OfTool: &tool}}
}
