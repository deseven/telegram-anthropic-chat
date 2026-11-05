package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// imageTool is a fake ToolExecutor whose generate_image succeeds or fails.
type imageTool struct {
	calls int
	fail  bool
}

func (f *imageTool) Tools() []anthropic.ToolUnionParam {
	return []anthropic.ToolUnionParam{{OfTool: &anthropic.ToolParam{
		Name:        "generate_image",
		InputSchema: anthropic.ToolInputSchemaParam{Properties: map[string]any{}},
	}}}
}

func (f *imageTool) Execute(ctx context.Context, name string, input json.RawMessage) ToolResult {
	f.calls++
	if f.fail {
		return ToolResult{Text: "ERROR: unsupported size", IsError: true}
	}
	return ToolResult{ImageB64: "QUJD", Image: []byte("ORIGINAL")}
}

// fakeAnthropic serves the given responses in order and records the request bodies.
func fakeAnthropic(t *testing.T, responses []string) (*Client, *[]string, *atomic.Int32) {
	t.Helper()
	var bodies []string
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		i := int(n.Add(1)) - 1
		if i >= len(responses) {
			t.Errorf("unexpected extra request #%d", i+1)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[i]))
	}))
	t.Cleanup(srv.Close)
	c := &Client{
		api:       anthropic.NewClient(option.WithBaseURL(srv.URL), option.WithAPIKey("k"), option.WithMaxRetries(0)),
		model:     "m",
		maxTokens: 100,
	}
	return c, &bodies, &n
}

func toolUseResponse(text string, uses ...string) string {
	content := []string{}
	if text != "" {
		content = append(content, `{"type":"text","text":"`+text+`"}`)
	}
	for i, name := range uses {
		id := "tu_" + string(rune('a'+i))
		content = append(content, `{"type":"tool_use","id":"`+id+`","name":"`+name+`","input":{"prompt":"cat"}}`)
	}
	return `{"id":"msg","type":"message","role":"assistant","model":"m","content":[` + strings.Join(content, ",") +
		`],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`
}

const finalResponse = `{"id":"msg","type":"message","role":"assistant","model":"m","content":[{"type":"text","text":"nice"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`

func TestToolImageDeliveredAndReturnedToModel(t *testing.T) {
	c, bodies, n := fakeAnthropic(t, []string{toolUseResponse("Drawing it now.", "generate_image"), finalResponse})
	tool := &imageTool{}
	msgs := []Message{{Role: "user", Blocks: []ContentBlock{{Text: "draw a cat"}}}}

	var texts []string
	var images [][]byte
	var started, ended []string
	reply, err := c.ChatWithTools(context.Background(), "sys", &msgs, tool, false, Callbacks{
		OnText:      func(s string) { texts = append(texts, s) },
		OnImage:     func(b []byte) { images = append(images, b) },
		OnToolStart: func(s string) { started = append(started, s) },
		OnToolEnd:   func(s string) { ended = append(ended, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	// The loop continues after the image: the model gets the result and
	// writes the final reply.
	if reply != "nice" {
		t.Errorf("reply = %q", reply)
	}
	if n.Load() != 2 {
		t.Errorf("expected 2 LLM requests, got %d", n.Load())
	}
	if len(texts) != 1 || texts[0] != "Drawing it now." {
		t.Errorf("texts = %v", texts)
	}
	if len(images) != 1 || string(images[0]) != "ORIGINAL" {
		t.Errorf("images = %q", images)
	}
	if len(started) != 1 || len(ended) != 1 || started[0] != "generate_image" {
		t.Errorf("tool hooks: started=%v ended=%v", started, ended)
	}
	// user, assistant(text+tool_use), user(tool_result with image), assistant(final)
	if len(msgs) != 4 {
		t.Fatalf("context has %d messages", len(msgs))
	}
	res := msgs[2].Blocks[0]
	if !res.IsToolResult() || res.ToolResultImageB64 != "QUJD" || res.ToolResultErr || res.ToolResultText != "" {
		t.Errorf("unexpected tool_result block: %+v", res)
	}

	// The second request must carry the image inside an image-only tool_result.
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type    string `json:"type"`
				Content []struct {
					Type   string `json:"type"`
					Source struct {
						MediaType string `json:"media_type"`
						Data      string `json:"data"`
					} `json:"source"`
				} `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte((*bodies)[1]), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d: %s", len(req.Messages), (*bodies)[1])
	}
	last := req.Messages[2]
	if last.Role != "user" || len(last.Content) != 1 || last.Content[0].Type != "tool_result" {
		t.Fatalf("unexpected last turn: %+v", last)
	}
	img := last.Content[0].Content
	if len(img) != 1 || img[0].Type != "image" || img[0].Source.MediaType != "image/jpeg" || img[0].Source.Data != "QUJD" {
		t.Errorf("tool_result content = %+v", img)
	}
}

func TestToolFailureReturnsToModel(t *testing.T) {
	c, _, n := fakeAnthropic(t, []string{toolUseResponse("", "generate_image"), finalResponse})
	tool := &imageTool{fail: true}
	msgs := []Message{{Role: "user", Blocks: []ContentBlock{{Text: "draw"}}}}
	images := 0
	reply, err := c.ChatWithTools(context.Background(), "sys", &msgs, tool, false, Callbacks{OnImage: func([]byte) { images++ }})
	if err != nil {
		t.Fatal(err)
	}
	if reply != "nice" || n.Load() != 2 || images != 0 {
		t.Errorf("reply=%q requests=%d images=%d", reply, n.Load(), images)
	}
	res := msgs[2].Blocks[0]
	if !res.ToolResultErr || !strings.Contains(res.ToolResultText, "unsupported size") {
		t.Errorf("error was not passed to the model: %+v", res)
	}
}

func TestMultipleImageCallsInOneBatch(t *testing.T) {
	c, _, n := fakeAnthropic(t, []string{toolUseResponse("", "generate_image", "generate_image"), finalResponse})
	tool := &imageTool{}
	msgs := []Message{{Role: "user", Blocks: []ContentBlock{{Text: "draw two"}}}}
	images := 0
	if _, err := c.ChatWithTools(context.Background(), "sys", &msgs, tool, false, Callbacks{OnImage: func([]byte) { images++ }}); err != nil {
		t.Fatal(err)
	}
	if tool.calls != 2 || images != 2 || n.Load() != 2 {
		t.Errorf("calls=%d images=%d requests=%d", tool.calls, images, n.Load())
	}
	results := msgs[2].Blocks
	if len(results) != 2 || results[0].ToolResultImageB64 == "" || results[1].ToolResultImageB64 == "" {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestNewToolSet(t *testing.T) {
	if NewToolSet() != nil || NewToolSet(nil, nil) != nil {
		t.Error("empty tool set must be a nil interface")
	}
	ts := NewToolSet(nil, &imageTool{})
	if ts == nil || len(ts.Tools()) != 1 {
		t.Fatalf("unexpected tool set: %v", ts)
	}
	if res := ts.Execute(context.Background(), "nope", nil); !res.IsError {
		t.Error("unknown tool should be an error")
	}
}

func TestSerializeHistoryGeneratedImage(t *testing.T) {
	out := SerializeHistory([]Message{{Role: "user", Blocks: []ContentBlock{{ToolResultID: "x", ToolResultText: "ok", ToolResultImageB64: "QUJD"}}}})
	if !strings.Contains(out, "[image generated by you]") {
		t.Errorf("history = %s", out)
	}
}

func TestRedactNestedImageData(t *testing.T) {
	var v any
	_ = json.Unmarshal([]byte(`{"messages":[{"content":[{"type":"tool_result","content":[{"type":"image","source":{"data":"SECRET"}}]}]}]}`), &v)
	redactImageData(v)
	b, _ := json.Marshal(v)
	if strings.Contains(string(b), "SECRET") {
		t.Errorf("image data not redacted: %s", b)
	}
}
