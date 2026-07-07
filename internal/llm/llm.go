// Package llm wraps the Anthropic Go SDK with the small surface area the bot
// needs: a chat completion over a user/assistant message array, and a memory
// extraction utility call.
package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/zoo/telegram-anthropic-chat/internal/log"
)

// Client wraps an Anthropic client with fixed model/maxTokens settings.
type Client struct {
	api       anthropic.Client
	model     string
	maxTokens int64
}

// maxToolIterations caps the number of tool-calling round trips in a single
// ChatWithTools call, preventing an infinite loop if the model keeps
// requesting tools.
const maxToolIterations = 10

// ToolExecutor provides the tools offered to the model and executes the tool
// calls the model makes. It is implemented by tool backends such as the
// Tavily web-search client.
type ToolExecutor interface {
	// Tools returns the Anthropic tool definitions to include in the request.
	Tools() []anthropic.ToolUnionParam
	// Execute runs the named tool with the given raw JSON input and returns
	// the result as a string (typically JSON) to feed back to the model as a
	// tool_result.
	Execute(ctx context.Context, name string, input json.RawMessage) string
}

// New creates a Client using the given API key, model and maxTokens. If
// dumpRequestsPath is non-empty, every Anthropic API request and response is
// appended to that file (the file is truncated on open).
func New(apiKey, model string, maxTokens int, dumpRequestsPath string) (*Client, error) {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}

	if dumpRequestsPath != "" {
		dumper, err := newRequestDumper(dumpRequestsPath)
		if err != nil {
			return nil, fmt.Errorf("open request dump file: %w", err)
		}
		opts = append(opts, option.WithMiddleware(dumper.middleware))
	}

	c := anthropic.NewClient(opts...)
	return &Client{api: c, model: model, maxTokens: int64(maxTokens)}, nil
}

// ContentBlock is a simplified content block for building requests. Only one
// of the fields below is set per block; the active field is determined by
// which is non-zero (ToolUseID/ToolUseName for a tool_use block,
// ToolResultID for a tool_result block, ImageB64 for an image, Text for
// text).
type ContentBlock struct {
	Text         string          // plain text content
	ImageB64     string          // base64-encoded JPEG (without data: prefix)
	ToolUseID    string          // tool_use: id assigned by the model
	ToolUseName  string          // tool_use: tool name
	ToolUseInput json.RawMessage // tool_use: raw JSON input
	ToolResultID   string        // tool_result: id of the tool_use it answers
	ToolResultText string        // tool_result: result content
	ToolResultErr  bool          // tool_result: whether the result is an error
}

// IsToolUse reports whether the block represents a tool_use.
func (b ContentBlock) IsToolUse() bool { return b.ToolUseID != "" }

// IsToolResult reports whether the block represents a tool_result.
func (b ContentBlock) IsToolResult() bool { return b.ToolResultID != "" }

// Message is a simplified user/assistant message.
type Message struct {
	Role   string // "user" or "assistant"
	Blocks []ContentBlock
}

// Chat sends the system prompt + messages and returns the assistant's text.
// The resulting assistant turn is appended to *msgsPtr.
func (c *Client) Chat(ctx context.Context, system string, msgsPtr *[]Message) (string, error) {
	return c.ChatWithTools(ctx, system, msgsPtr, nil)
}

// ChatWithTools is like Chat but, when tools is non-nil, offers the given
// tools to the model and runs the tool-calling loop to completion.
//
// msgsPtr points to the caller's conversation slice. The loop appends every
// assistant turn (text and tool_use blocks) and every user tool_result turn
// to *msgsPtr as it goes, so the caller's stored context reflects the full
// tool-calling flow for future turns. The final (non-tool) assistant turn is
// also appended, so the caller does not need to append it separately.
//
// The onText callback (if non-nil) is invoked with intermediate text the
// model emits before a tool call (e.g. "Let me look that up") so the caller
// can forward it to the user immediately. The final turn's text is returned
// as the result string (and is NOT forwarded via the callback), so the
// caller sends it exactly once.
//
// If tools is nil, the call behaves exactly like Chat (no tool loop, no
// callback), and the single assistant turn is appended to *msgsPtr.
func (c *Client) ChatWithTools(ctx context.Context, system string, msgsPtr *[]Message, tools ToolExecutor, onText ...func(string)) (string, error) {
	var textCb func(string)
	if len(onText) > 0 {
		textCb = onText[0]
	}

	toolDefs := []anthropic.ToolUnionParam(nil)
	if tools != nil {
		toolDefs = tools.Tools()
	}

	var finalText string

	for iter := 0; ; iter++ {
		conversation := toParams(*msgsPtr)
		resp, err := c.api.Messages.New(ctx, anthropic.MessageNewParams{
			Model:     anthropic.Model(c.model),
			MaxTokens: c.maxTokens,
			System: []anthropic.TextBlockParam{
				{Text: system},
			},
			Messages: conversation,
			Tools:    toolDefs,
		})
		if err != nil {
			var apierr *anthropic.Error
			if errors.As(err, &apierr) {
				log.Print("llm", "api error %d (req %s): %s", apierr.StatusCode, apierr.RequestID, string(apierr.DumpResponse(false)))
			}
			return "", fmt.Errorf("anthropic messages: %w", err)
		}

		// Collect text and tool-use blocks from the response.
		var turnText string
		var toolUses []anthropic.ToolUseBlock
		for _, block := range resp.Content {
			switch v := block.AsAny().(type) {
			case anthropic.TextBlock:
				turnText += v.Text
			case anthropic.ToolUseBlock:
				toolUses = append(toolUses, v)
			}
		}

		// If the model made no tool calls, the turn is complete: this is
		// the final reply. Append it to the context and return it (not
		// forwarded via the callback) so the caller sends it exactly once.
		if len(toolUses) == 0 || resp.StopReason != anthropic.StopReasonToolUse {
			if turnText != "" {
				log.Print("llm", "final response (%d chars)", len(turnText))
			}
			if turnText != "" || len(resp.Content) > 0 {
				*msgsPtr = append(*msgsPtr, llmAssistantText(turnText))
			}
			finalText = turnText
			break
		}

		// Intermediate turn: the model is about to call tools. Build the
		// assistant message (text + tool_use blocks) and append it to the
		// context so the flow is recorded for future turns.
		assistantBlocks := make([]ContentBlock, 0, 1+len(toolUses))
		if turnText != "" {
			assistantBlocks = append(assistantBlocks, ContentBlock{Text: turnText})
		}
		for _, tu := range toolUses {
			assistantBlocks = append(assistantBlocks, ContentBlock{
				ToolUseID:    tu.ID,
				ToolUseName:  tu.Name,
				ToolUseInput: append(json.RawMessage(nil), tu.Input...),
			})
		}
		*msgsPtr = append(*msgsPtr, Message{Role: "assistant", Blocks: assistantBlocks})

		// Forward any accompanying text to the caller immediately so the
		// user sees it while the tool runs. This text is NOT part of the
		// returned reply — it has already been delivered.
		if turnText != "" {
			log.Print("llm", "intermediate text (%d chars) before %d tool call(s)", len(turnText), len(toolUses))
			if textCb != nil {
				textCb(turnText)
			}
		} else {
			log.Print("llm", "model requested %d tool call(s)", len(toolUses))
		}

		if tools == nil {
			// Defensive: the model requested tools but none were provided.
			break
		}

		// Execute each tool call and build the user tool_result turn. When
		// the iteration cap is reached, return an error result for every
		// pending tool call instructing the model to stop calling tools
		// and answer the user with what it has so far.
		resultBlocks := make([]ContentBlock, 0, len(toolUses))
		if iter >= maxToolIterations {
			log.Print("llm", "tool loop reached max iterations (%d), asking model to stop", maxToolIterations)
			for _, tu := range toolUses {
				resultBlocks = append(resultBlocks, ContentBlock{
					ToolResultID:   tu.ID,
					ToolResultText: "ERROR: the maximum number of tool calls in a single turn has been reached. Do not call any more tools; respond to the user now using the information gathered so far or just ask to confirm that you can continue if needed, user reply will start a new turn.",
					ToolResultErr:  true,
				})
			}
			*msgsPtr = append(*msgsPtr, Message{Role: "user", Blocks: resultBlocks})
			continue
		}

		for _, tu := range toolUses {
			result := tools.Execute(ctx, tu.Name, tu.Input)
			resultBlocks = append(resultBlocks, ContentBlock{
				ToolResultID:   tu.ID,
				ToolResultText: result,
			})
		}
		*msgsPtr = append(*msgsPtr, Message{Role: "user", Blocks: resultBlocks})
	}

	return finalText, nil
}

// llmAssistantText builds a single-text assistant message.
func llmAssistantText(text string) Message {
	return Message{Role: "assistant", Blocks: []ContentBlock{{Text: text}}}
}

// ExtractedMemory is a memory returned by the extraction prompt.
type ExtractedMemory struct {
	Importance int    `json:"importance"`
	Text       string `json:"text"`
}

type extractionResponse struct {
	Memories []ExtractedMemory `json:"memories"`
}

// ExtractMemoriesFromText asks the model to extract memories from a single
// user message (the rendered memories-user prompt) using the provided memories
// system prompt. This is the two-prompt variant of ExtractMemories.
func (c *Client) ExtractMemoriesFromText(ctx context.Context, system, user string) ([]ExtractedMemory, error) {
	msgs := []Message{{Role: "user", Blocks: []ContentBlock{{Text: user}}}}
	text, err := c.Chat(ctx, system, &msgs)
	if err != nil {
		return nil, err
	}
	cleaned := stripJSONFences(text)
	var res extractionResponse
	if err := json.Unmarshal([]byte(cleaned), &res); err != nil {
		return nil, fmt.Errorf("parse memories json: %w (raw: %s)", err, truncate(text, 300))
	}
	return res.Memories, nil
}

// historyEntry is a single message in the serialized [HISTORY] block.
type historyEntry struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// SerializeHistory renders a slice of messages as a JSON array of
// {role, content} objects suitable for the [HISTORY] block of the memories
// user prompt. Image blocks are represented as "[image]" placeholders.
//
// Tool-calling turns are summarized: a tool_use is recorded with its name and
// parameters (so the memory model knows what was looked up), while the
// matching tool_result is recorded only as an acknowledgment — the raw result
// payload is omitted to keep the history focused on the conversation.
func SerializeHistory(msgs []Message) string {
	entries := make([]historyEntry, 0, len(msgs))
	for _, m := range msgs {
		var b strings.Builder
		for _, blk := range m.Blocks {
			switch {
			case blk.IsToolUse():
				fmt.Fprintf(&b, "[tool call: %s(%s)]", blk.ToolUseName, string(blk.ToolUseInput))
			case blk.IsToolResult():
				if blk.ToolResultErr {
					b.WriteString("[tool call failed]")
				} else {
					b.WriteString("[tool call completed]")
				}
			case blk.Text != "":
				b.WriteString(blk.Text)
			case blk.ImageB64 != "":
				b.WriteString("[image]")
			}
		}
		entries = append(entries, historyEntry{Role: m.Role, Content: b.String()})
	}
	out, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		// MarshalIndent never fails for this struct shape.
		return "[]"
	}
	return string(out)
}

func toParams(msgs []Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		blocks := make([]anthropic.ContentBlockParamUnion, 0, len(m.Blocks))
		for _, b := range m.Blocks {
			switch {
			case b.IsToolUse():
				// Re-serialize the stored raw JSON input. If it is empty or
				// invalid, fall back to an empty object so the request still
				// marshals.
				var input any
				if len(b.ToolUseInput) > 0 {
					if err := json.Unmarshal(b.ToolUseInput, &input); err != nil {
						input = map[string]any{}
					}
				} else {
					input = map[string]any{}
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ToolUseID, input, b.ToolUseName))
			case b.IsToolResult():
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolResultID, b.ToolResultText, b.ToolResultErr))
			case b.ImageB64 != "":
				blocks = append(blocks, anthropic.NewImageBlockBase64("image/jpeg", b.ImageB64))
			case b.Text != "":
				blocks = append(blocks, anthropic.NewTextBlock(b.Text))
			}
		}
		if len(blocks) == 0 {
			continue
		}
		role := anthropic.MessageParamRoleUser
		if m.Role == "assistant" {
			role = anthropic.MessageParamRoleAssistant
		}
		out = append(out, anthropic.MessageParam{
			Role:    role,
			Content: blocks,
		})
	}
	return out
}

// stripJSONFences removes markdown code fences (``` or ```json) that a model
// may wrap around a JSON response, even when surrounded by commentary. It finds
// the first opening fence and the last closing fence and returns whatever is
// between them. If no fences are present, the trimmed input is returned as-is.
func stripJSONFences(s string) string {
	s = trimSpace(s)

	// Find the first opening fence.
	open := findFence(s, 0)
	if open < 0 {
		return s
	}
	// Skip past the opening fence line (the ``` and any language tag like json).
	rest := s[open+3:]
	// Strip an optional language identifier on the same line as the opening fence.
	if nl := indexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	} else {
		// Opening fence with no newline: nothing useful after it.
		return trimSpace(rest)
	}

	// Find the last closing fence within the remainder.
	close := findLastFence(rest)
	if close < 0 {
		return trimSpace(rest)
	}
	return trimSpace(rest[:close])
}

// findFence returns the index of the first "```" occurrence at or after start,
// or -1 if none.
func findFence(s string, start int) int {
	for i := start; i <= len(s)-3; i++ {
		if s[i] == '`' && s[i+1] == '`' && s[i+2] == '`' {
			return i
		}
	}
	return -1
}

// findLastFence returns the index of the last "```" occurrence in s, or -1.
func findLastFence(s string) int {
	last := -1
	for i := 0; i <= len(s)-3; i++ {
		if s[i] == '`' && s[i+1] == '`' && s[i+2] == '`' {
			last = i
		}
	}
	return last
}

// EncodeImage is a convenience helper used by callers that already have raw
// image bytes and want a base64 string for a ContentBlock.
func EncodeImage(raw []byte) string {
	return base64.StdEncoding.EncodeToString(raw)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// minimal string helpers to avoid pulling strings package for tiny ops.
func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\n' || s[start] == '\r' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\n' || s[end-1] == '\r' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

// requestDumper appends a textual dump of every Anthropic HTTP request and
// response to a file. It is wired into the SDK via option.WithMiddleware, so
// it captures the exact bytes sent to and received from the API for both chat
// and memory-extraction calls. Note: the dump includes the full request body
// and headers (including the API key) — keep the dump file private.
type requestDumper struct {
	mu sync.Mutex
	f  *os.File
}

// newRequestDumper opens path for writing, truncating any prior content so
// each application run starts with a clean file.
func newRequestDumper(path string) (*requestDumper, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}
	return &requestDumper{f: f}, nil
}

// middleware is the option.Middleware hook. It dumps the outgoing request,
// forwards it to the next stage, then dumps the response (or error). The JSON
// bodies of both request and response are pretty-printed for readability, and
// any base64 image payload found at .messages[].content[].source.data is
// replaced with the placeholder "image_data" to keep the dump concise.
func (d *requestDumper) middleware(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
	ts := time.Now().Format("2006-01-02 15:04:05.000")

	reqDump, dumpErr := dumpRequestPretty(req)
	if dumpErr != nil {
		log.Print("llm", "dump request failed: %v", dumpErr)
	}

	resp, err := next(req)

	d.mu.Lock()
	defer d.mu.Unlock()

	if dumpErr == nil {
		fmt.Fprintf(d.f, "[%s] >>> REQUEST >>>\n%s\n", ts, reqDump)
	}

	if err != nil {
		fmt.Fprintf(d.f, "[%s] <<< ERROR <<<\n%v\n\n", ts, err)
		return resp, err
	}

	if resp != nil {
		respDump, rdErr := dumpResponsePretty(resp)
		if rdErr != nil {
			log.Print("llm", "dump response failed: %v", rdErr)
			fmt.Fprintf(d.f, "[%s] <<< RESPONSE (dump failed) <<<\n%v\n\n", ts, rdErr)
		} else {
			fmt.Fprintf(d.f, "[%s] <<< RESPONSE <<<\n%s\n\n", ts, respDump)
		}
	}
	return resp, err
}

// dumpRequestPretty returns a human-readable dump of req. The request body is
// read, JSON-pretty-printed (with image data redacted), and then restored so
// the request can still be sent over the wire.
func dumpRequestPretty(req *http.Request) (string, error) {
	var body string
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return "", fmt.Errorf("read request body: %w", err)
		}
		req.Body = io.NopCloser(bytes.NewReader(raw))
		body = prettyJSON(raw)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s HTTP/1.1\r\n", req.Method, req.URL.RequestURI())
	fmt.Fprintf(&b, "Host: %s\r\n", req.URL.Host)
	req.Header.Write(&b)
	if body != "" {
		fmt.Fprintf(&b, "\r\n%s", body)
	}
	return b.String(), nil
}

// dumpResponsePretty returns a human-readable dump of resp. The response body
// is read, JSON-pretty-printed (with image data redacted), and then restored
// so the caller can still consume it.
func dumpResponsePretty(resp *http.Response) (string, error) {
	raw, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return "", fmt.Errorf("read response body: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))

	var b strings.Builder
	fmt.Fprintf(&b, "HTTP/%d.%d %s\r\n", resp.ProtoMajor, resp.ProtoMinor, resp.Status)
	resp.Header.Write(&b)
	fmt.Fprintf(&b, "\r\n%s", prettyJSON(raw))
	return b.String(), nil
}

// prettyJSON returns a pretty-printed (indented) version of raw if it is valid
// JSON, with any base64 image payload at .messages[].content[].source.data
// replaced by the placeholder "image_data". If raw is not valid JSON it is
// returned unchanged.
func prettyJSON(raw []byte) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return string(raw)
	}
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return string(raw)
	}
	redactImageData(v)
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return string(raw)
	}
	return string(out)
}

// redactImageData walks the parsed JSON value and replaces any
// .messages[].content[].source.data string with "image_data".
func redactImageData(v any) {
	root, ok := v.(map[string]any)
	if !ok {
		return
	}
	msgs, ok := root["messages"].([]any)
	if !ok {
		return
	}
	for _, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"].([]any)
		if !ok {
			continue
		}
		for _, c := range content {
			block, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if src, ok := block["source"].(map[string]any); ok {
				if _, hasData := src["data"]; hasData {
					src["data"] = "image_data"
				}
			}
		}
	}
}
