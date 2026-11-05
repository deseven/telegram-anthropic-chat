package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zoo/telegram-anthropic-chat/internal/config"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newClient(endpoint string) *Client {
	return New(&config.ImageGenerationConfig{
		APIEndpoint: endpoint, Model: "test-model", Timeout: 5,
		DefaultWidth: 512, DefaultHeight: 512, MaxWidth: 1024, MaxHeight: 1024,
	})
}

func TestGenerateRequestAndSuccess(t *testing.T) {
	pngBytes := testPNG(t, 64, 48)
	var gotBody map[string]any
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(pngBytes)}},
		})
	}))
	defer srv.Close()

	c := newClient(srv.URL + "/api")
	c.cfg.APIKey = "secret"
	c.cfg.ExtraParams = map[string]any{"quality": "low", "model": "should-be-overridden"}

	res := c.Execute(context.Background(), ToolName, json.RawMessage(`{"prompt":"a cat","width":64,"height":48}`))
	if res.IsError {
		t.Fatalf("unexpected result: %+v", res)
	}
	if !bytes.Equal(res.Image, pngBytes) {
		t.Error("original image bytes were not passed through unchanged")
	}
	if res.ImageB64 == "" {
		t.Error("missing processed image for the model")
	}
	if res.Text != "" {
		t.Errorf("successful result should carry only the image, got text %q", res.Text)
	}
	if gotPath != "/api/v1/images/generations" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotBody["model"] != "test-model" || gotBody["prompt"] != "a cat" || gotBody["size"] != "64x48" || gotBody["n"] != float64(1) || gotBody["quality"] != "low" {
		t.Errorf("unexpected body: %v", gotBody)
	}
	if _, ok := gotBody["response_format"]; ok {
		t.Error("response_format must be omitted by default")
	}
}

func TestDefaultSizeAndNoAuth(t *testing.T) {
	var gotBody map[string]any
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(testPNG(t, 8, 8))}},
		})
	}))
	defer srv.Close()
	c := newClient(srv.URL)
	c.cfg.ResponseFormat = "b64_json"
	res := c.Execute(context.Background(), ToolName, json.RawMessage(`{"prompt":"x"}`))
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Text)
	}
	if gotBody["size"] != "512x512" || gotBody["response_format"] != "b64_json" {
		t.Errorf("unexpected body: %v", gotBody)
	}
	if gotAuth != "" {
		t.Errorf("no Authorization header expected, got %q", gotAuth)
	}
}

func TestURLResponse(t *testing.T) {
	pngBytes := testPNG(t, 16, 16)
	mux := http.NewServeMux()
	mux.HandleFunc("/img.png", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(pngBytes) })
	var srvURL string
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]any{{"url": srvURL + "/img.png"}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	srvURL = srv.URL

	raw, err := newClient(srv.URL).Generate(context.Background(), "x", 16, 16)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, pngBytes) {
		t.Error("downloaded bytes differ")
	}
}

func TestErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"openai error body", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_size","message":"size 7x7 is not supported"}}`))
		}, "HTTP 400: invalid_size: size 7x7 is not supported"},
		{"plain error body", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(502)
			_, _ = w.Write([]byte("bad gateway"))
		}, "HTTP 502: bad gateway"},
		{"error in 200", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"error":"out of GPUs"}`))
		}, "out of GPUs"},
		{"empty data", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"data":[]}`)) }, "no images"},
		{"not json", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`<html>`)) }, "invalid provider response"},
		{"bad base64", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":[{"b64_json":"!!!"}]}`))
		}, "invalid base64"},
		{"not an image", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"data":[{"b64_json":"` + base64.StdEncoding.EncodeToString([]byte("hello")) + `"}]}`))
		}, "unusable image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			res := newClient(srv.URL).Execute(context.Background(), ToolName, json.RawMessage(`{"prompt":"x"}`))
			if !res.IsError || res.Image != nil || res.ImageB64 != "" {
				t.Fatalf("expected a plain error result, got %+v", res)
			}
			if !strings.Contains(res.Text, tc.want) {
				t.Errorf("text %q does not contain %q", res.Text, tc.want)
			}
		})
	}
}

func TestTimeout(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)

	c := newClient(srv.URL)
	c.cfg.Timeout = 1
	start := time.Now()
	res := c.Execute(context.Background(), ToolName, json.RawMessage(`{"prompt":"x"}`))
	if !res.IsError || !strings.Contains(res.Text, "timed out after 1s") {
		t.Fatalf("unexpected result: %+v", res)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("timeout took too long: %s", time.Since(start))
	}
}

func TestProviderDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	res := newClient(url).Execute(context.Background(), ToolName, json.RawMessage(`{"prompt":"x"}`))
	if !res.IsError || !strings.Contains(res.Text, "request:") {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestInputValidation(t *testing.T) {
	c := newClient("http://127.0.0.1:1") // never reached
	cases := map[string]string{
		`{}`:                            "prompt is required",
		`{"prompt":"  "}`:               "prompt is required",
		`{"prompt":"x","width":2048}`:   "width must be between 1 and 1024",
		`{"prompt":"x","height":0}`:     "height must be between 1 and 1024",
		`{"prompt":"x","width":100.5}`:  "integer",
		`{"prompt":"x","width":"wide"}`: "invalid input",
		`not json`:                      "invalid input",
	}
	for in, want := range cases {
		res := c.Execute(context.Background(), ToolName, json.RawMessage(in))
		if !res.IsError || !strings.Contains(res.Text, want) {
			t.Errorf("%s: got %+v, want error containing %q", in, res, want)
		}
	}
}

func TestToolDefinition(t *testing.T) {
	tools := newClient("http://x").Tools()
	if len(tools) != 1 || tools[0].OfTool == nil || tools[0].OfTool.Name != ToolName {
		t.Fatalf("unexpected tools: %+v", tools)
	}
	if req := tools[0].OfTool.InputSchema.Required; len(req) != 1 || req[0] != "prompt" {
		t.Errorf("required = %v", req)
	}
	desc := tools[0].OfTool.Description.Value
	for _, want := range []string{"English", "512x512", "1024x1024", "returned to you as the tool result"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description is missing %q", want)
		}
	}
}

// TestLive talks to a real provider. Run with, e.g.:
//
//	IMAGEGEN_LIVE_ENDPOINT=http://10.66.66.2:13305/api IMAGEGEN_LIVE_MODEL=Kroma-Turbo-8 go test ./internal/imagegen -run Live -v
func TestLive(t *testing.T) {
	endpoint := os.Getenv("IMAGEGEN_LIVE_ENDPOINT")
	if endpoint == "" {
		t.Skip("IMAGEGEN_LIVE_ENDPOINT not set")
	}
	c := New(&config.ImageGenerationConfig{
		APIEndpoint: endpoint, Model: os.Getenv("IMAGEGEN_LIVE_MODEL"), APIKey: os.Getenv("IMAGEGEN_LIVE_KEY"),
		Timeout: 60, DefaultWidth: 512, DefaultHeight: 512, MaxWidth: 1024, MaxHeight: 1024,
	})
	res := c.Execute(context.Background(), ToolName, json.RawMessage(`{"prompt":"a red apple on a wooden table, studio photo","width":256,"height":256}`))
	if res.IsError {
		t.Fatalf("live generation failed: %s", res.Text)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(res.Image))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("got %s %dx%d, %d bytes", format, cfg.Width, cfg.Height, len(res.Image))
	if cfg.Width != 256 || cfg.Height != 256 {
		t.Errorf("unexpected size %dx%d", cfg.Width, cfg.Height)
	}
}
