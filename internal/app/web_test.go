package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zoo/telegram-anthropic-chat/internal/config"
	"github.com/zoo/telegram-anthropic-chat/internal/storage"
)

// newTestApp builds an App with only the dependencies the web handlers need,
// running the global queue worker so submitSync jobs execute.
func newTestApp(t *testing.T) (*App, *storage.Store) {
	t.Helper()
	store, err := storage.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &App{
		cfg:       &config.Config{MemoriesCtxSize: 16384, HTTPPublicURL: "https://example.com"},
		store:     store,
		sessions:  make(map[int64]*session),
		jobCh:     make(chan job, 256),
		webCodes:  make(map[string]webAuthCode),
		webTokens: make(map[string]int64),
	}
	go a.worker()
	return a, store
}

func writeUser(t *testing.T, store *storage.Store, userID int64) {
	t.Helper()
	ud := &storage.UserData{UserDescription: "test user"}
	ud.AddMemories([]storage.Memory{{Importance: 5, Text: "likes tea"}})
	if err := store.Save(userID, ud); err != nil {
		t.Fatal(err)
	}
}

func TestWebFlow(t *testing.T) {
	a, store := newTestApp(t)
	writeUser(t, store, 42)

	mux := http.NewServeMux()
	a.RegisterWebRoutes(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	do := func(method, path, token string, body any) (int, map[string]any) {
		t.Helper()
		var rdr *bytes.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			rdr = bytes.NewReader(raw)
		} else {
			rdr = bytes.NewReader(nil)
		}
		req, err := http.NewRequest(method, srv.URL+path, rdr)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// The index page is served at the root.
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("index: status %d", resp.StatusCode)
	}

	// Auth with a bogus code is rejected.
	if status, _ := do(http.MethodPost, "/api/auth", "", map[string]string{"code": "bogus"}); status != http.StatusUnauthorized {
		t.Fatalf("bogus code: status %d", status)
	}

	// A real code exchanges for a token.
	code := a.newWebCode(42)
	status, out := do(http.MethodPost, "/api/auth", "", map[string]string{"code": code})
	if status != http.StatusOK {
		t.Fatalf("auth: status %d, body %v", status, out)
	}
	token, _ := out["token"].(string)
	if token == "" {
		t.Fatal("auth: empty token")
	}

	// Codes are single-use.
	if status, _ := do(http.MethodPost, "/api/auth", "", map[string]string{"code": code}); status != http.StatusUnauthorized {
		t.Fatalf("code reuse: status %d", status)
	}

	// getInfo returns the user data and memories.
	status, out = do(http.MethodGet, "/api/getInfo", token, nil)
	if status != http.StatusOK {
		t.Fatalf("getInfo: status %d, body %v", status, out)
	}
	if out["userDescription"] != "test user" {
		t.Fatalf("getInfo: description %v", out["userDescription"])
	}
	active, _ := out["activeMemories"].([]any)
	if len(active) != 1 {
		t.Fatalf("getInfo: %d active memories, want 1", len(active))
	}

	// Add a memory.
	status, out = do(http.MethodPost, "/api/memory", token, map[string]any{"text": "has a cat", "importance": 7})
	if status != http.StatusOK {
		t.Fatalf("add: status %d, body %v", status, out)
	}
	if id, _ := out["id"].(float64); id != 2 {
		t.Fatalf("add: id %v, want 2", out["id"])
	}

	// Edit it.
	status, out = do(http.MethodPost, "/api/memory/2/edit", token, map[string]any{"text": "has two cats", "importance": 8})
	if status != http.StatusOK {
		t.Fatalf("edit: status %d, body %v", status, out)
	}
	if out["text"] != "has two cats" {
		t.Fatalf("edit: text %v", out["text"])
	}

	// Delete it.
	if status, _ := do(http.MethodPost, "/api/memory/2/delete", token, nil); status != http.StatusOK {
		t.Fatalf("delete: status %d", status)
	}
	if status, _ := do(http.MethodPost, "/api/memory/2/delete", token, nil); status != http.StatusNotFound {
		t.Fatalf("delete missing: status %d", status)
	}

	// Changes are persisted to disk.
	ud, err := store.Load(42)
	if err != nil {
		t.Fatal(err)
	}
	if len(ud.Memories) != 1 || ud.Memories[0].Text != "likes tea" {
		t.Fatalf("persisted memories: %+v", ud.Memories)
	}
	if len(ud.WebTokens) != 1 || ud.WebTokens[0] != token {
		t.Fatalf("persisted tokens: %+v", ud.WebTokens)
	}

	// A bad token is rejected; a missing one too.
	if status, _ := do(http.MethodGet, "/api/getInfo", "nope", nil); status != http.StatusUnauthorized {
		t.Fatalf("bad token: status %d", status)
	}
	if status, _ := do(http.MethodGet, "/api/getInfo", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("no token: status %d", status)
	}
}

func TestWebCodeExpiry(t *testing.T) {
	a, store := newTestApp(t)
	writeUser(t, store, 42)

	code := a.newWebCode(42)
	a.webMu.Lock()
	ac := a.webCodes[code]
	ac.expires = time.Now().Add(-time.Second)
	a.webCodes[code] = ac
	a.webMu.Unlock()

	var err error
	a.submitSync(func() {
		_, err = a.exchangeWebCode(code)
	})
	if err == nil {
		t.Fatal("expected error for expired code")
	}
}
