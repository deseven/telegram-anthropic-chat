// Web interface: a single-page app for managing memories in the browser,
// served by the same HTTP server as the Telegram webhook.
//
// Auth flow: the user sends /web to the bot and receives a link containing a
// temporary 16-symbol code (valid for webCodeTTL). The web app exchanges the
// code (POST /web/api/auth) for a permanent token, which it stores in the
// browser's localStorage and sends as a Bearer token on every API request.
// Permanent tokens are persisted in the user data file so they survive
// restarts.
package app

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zoo/telegram-anthropic-chat/internal/log"
	"github.com/zoo/telegram-anthropic-chat/internal/memories"
	"github.com/zoo/telegram-anthropic-chat/internal/storage"
)

//go:embed web/index.html
var webIndexHTML []byte

const (
	// webCodeLen is the length of the temporary auth code sent by /web.
	webCodeLen = 16
	// webCodeTTL is how long a temporary auth code stays valid.
	webCodeTTL = 60 * time.Second
	// webTokenLen is the length of a permanent web token.
	webTokenLen = 32
	// webAlphabet is the character set for codes and tokens.
	webAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
)

// webAuthCode is a temporary, one-time code that exchanges for a token.
type webAuthCode struct {
	userID  int64
	expires time.Time
}

// webAPIError carries an HTTP status and a user-facing message.
type webAPIError struct {
	status int
	msg    string
}

// webMemory is the API representation of a single memory.
type webMemory struct {
	ID         int    `json:"id"`
	Importance int    `json:"importance"`
	Text       string `json:"text"`
	Date       int64  `json:"date"`
}

// webSessionInfo describes the user's active chat session, if any.
type webSessionInfo struct {
	Active    bool      `json:"active"`
	Messages  int       `json:"messages"`
	StartedAt time.Time `json:"startedAt"`
}

// webInfo is the response of GET /web/api/getInfo.
type webInfo struct {
	UserID          int64           `json:"userId"`
	UserDescription string          `json:"userDescription"`
	Session         *webSessionInfo `json:"session"`
	ActiveMemories  []webMemory     `json:"activeMemories"`
	OtherMemories   []webMemory     `json:"otherMemories"`
}

// RegisterWebRoutes registers the web interface and its API on mux. The
// single-page app is served at the root path; the API lives under /api.
func (a *App) RegisterWebRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", a.handleWebIndex)
	mux.HandleFunc("POST /api/auth", a.handleWebAuth)
	mux.HandleFunc("GET /api/getInfo", a.handleWebGetInfo)
	mux.HandleFunc("POST /api/memory", a.handleWebMemoryAdd)
	mux.HandleFunc("POST /api/memory/{id}/edit", a.handleWebMemoryEdit)
	mux.HandleFunc("POST /api/memory/{id}/delete", a.handleWebMemoryDelete)
}

// cmdWeb implements the /web command: it generates a temporary auth code and
// sends the user a personal link to the web interface. /mem is a deprecated
// alias of this command.
func (a *App) cmdWeb(ctx context.Context, chatID int64, sess *session) {
	if a.cfg.HTTPPublicURL == "" {
		a.sendText(ctx, chatID, "The web interface is not available: httpPublicURL is not configured.")
		return
	}
	code := a.newWebCode(sess.userID)
	url := a.cfg.HTTPPublicURL + "/?code=" + code
	a.sendText(ctx, chatID, fmt.Sprintf("Manage your memories in the browser (link valid for %d seconds):\n%s", int(webCodeTTL.Seconds()), url))
}

// newWebCode generates and stores a temporary auth code for the user.
func (a *App) newWebCode(userID int64) string {
	code := randomString(webCodeLen)
	now := time.Now()
	a.webMu.Lock()
	// Prune expired codes while we're here.
	for c, ac := range a.webCodes {
		if now.After(ac.expires) {
			delete(a.webCodes, c)
		}
	}
	a.webCodes[code] = webAuthCode{userID: userID, expires: now.Add(webCodeTTL)}
	a.webMu.Unlock()
	return code
}

// exchangeWebCode converts a temporary code into a permanent token. The code
// is single-use: it is consumed even when expired. The token is persisted in
// the user data file. Must be called from the global queue worker.
func (a *App) exchangeWebCode(code string) (string, error) {
	a.webMu.Lock()
	ac, ok := a.webCodes[code]
	if ok {
		delete(a.webCodes, code)
	}
	a.webMu.Unlock()

	if !ok || time.Now().After(ac.expires) {
		return "", errors.New("invalid or expired code — send /web to the bot to get a new link")
	}
	if !a.store.Exists(ac.userID) {
		return "", errors.New("user is no longer allowed")
	}
	sess, err := a.getOrCreateSession(ac.userID)
	if err != nil {
		return "", errors.New("couldn't load user data")
	}
	token := randomString(webTokenLen)
	sess.data.WebTokens = append(sess.data.WebTokens, token)
	if err := a.store.Save(ac.userID, sess.data); err != nil {
		log.Print("web", "save after token creation failed for %d: %v", ac.userID, err)
		return "", errors.New("couldn't save the token")
	}
	a.webMu.Lock()
	a.webTokens[token] = ac.userID
	a.webMu.Unlock()
	log.Print("web", "issued web token for user %d", ac.userID)
	return token, nil
}

// resolveWebToken maps a permanent token to a user id. On a cache miss (e.g.
// after a restart) it scans all user data files and re-populates the cache.
// Must be called from the global queue worker.
func (a *App) resolveWebToken(token string) (int64, bool) {
	a.webMu.Lock()
	id, ok := a.webTokens[token]
	a.webMu.Unlock()
	if ok {
		return id, true
	}

	ids, err := a.store.ListUsers()
	if err != nil {
		log.Print("web", "list users failed: %v", err)
		return 0, false
	}
	var found int64
	for _, id := range ids {
		ud, err := a.store.Load(id)
		if err != nil {
			continue
		}
		a.webMu.Lock()
		for _, t := range ud.WebTokens {
			a.webTokens[t] = id
			if t == token {
				found = id
			}
		}
		a.webMu.Unlock()
	}
	return found, found != 0
}

// webAuthSession resolves a Bearer token to the user's session, verifying the
// user is still whitelisted. Must be called from the global queue worker.
func (a *App) webAuthSession(token string) (*session, *webAPIError) {
	userID, ok := a.resolveWebToken(token)
	if !ok {
		return nil, &webAPIError{http.StatusUnauthorized, "invalid token — send /web to the bot to get a new link"}
	}
	if !a.store.Exists(userID) {
		return nil, &webAPIError{http.StatusForbidden, "your user is no longer allowed on this bot"}
	}
	sess, err := a.getOrCreateSession(userID)
	if err != nil {
		log.Print("web", "session load failed for %d: %v", userID, err)
		return nil, &webAPIError{http.StatusInternalServerError, "couldn't load user data"}
	}
	return sess, nil
}

// handleWebIndex serves the embedded single-page app.
func (a *App) handleWebIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(webIndexHTML)
}

// handleWebAuth exchanges a temporary code for a permanent token.
func (a *App) handleWebAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Code) == "" {
		writeWebErr(w, http.StatusBadRequest, "missing code")
		return
	}
	var token string
	var err error
	a.submitSync(func() {
		token, err = a.exchangeWebCode(strings.TrimSpace(req.Code))
	})
	if err != nil {
		writeWebErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]string{"token": token})
}

// handleWebGetInfo returns the user data, session stats and memories.
func (a *App) handleWebGetInfo(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeWebErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	var info *webInfo
	var apiErr *webAPIError
	a.submitSync(func() {
		sess, err := a.webAuthSession(token)
		if err != nil {
			apiErr = err
			return
		}
		in, out := memories.Split(sess.data.Memories, a.cfg.MemoriesCtxSize)
		info = &webInfo{
			UserID:          sess.userID,
			UserDescription: sess.data.UserDescription,
			ActiveMemories:  toWebMemories(in),
			OtherMemories:   toWebMemories(out),
		}
		if len(sess.ctx) > 0 {
			info.Session = &webSessionInfo{
				Active:    true,
				Messages:  len(sess.ctx),
				StartedAt: sess.sessionStart,
			}
		}
	})
	if apiErr != nil {
		writeWebErr(w, apiErr.status, apiErr.msg)
		return
	}
	writeWebJSON(w, http.StatusOK, info)
}

// webMemoryReq is the request body for adding or editing a memory.
type webMemoryReq struct {
	Text       string `json:"text"`
	Importance int    `json:"importance"`
}

// handleWebMemoryAdd creates a new memory.
func (a *App) handleWebMemoryAdd(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeWebErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	req, ok := parseWebMemoryReq(w, r)
	if !ok {
		return
	}
	var created webMemory
	var apiErr *webAPIError
	a.submitSync(func() {
		sess, err := a.webAuthSession(token)
		if err != nil {
			apiErr = err
			return
		}
		sess.data.AddMemories([]storage.Memory{{Importance: req.Importance, Text: req.Text}})
		if saveErr := a.store.Save(sess.userID, sess.data); saveErr != nil {
			log.Print("web", "save after add failed for %d: %v", sess.userID, saveErr)
			apiErr = &webAPIError{http.StatusInternalServerError, "couldn't save the memory"}
			return
		}
		m := sess.data.Memories[len(sess.data.Memories)-1]
		created = toWebMemory(m)
	})
	if apiErr != nil {
		writeWebErr(w, apiErr.status, apiErr.msg)
		return
	}
	writeWebJSON(w, http.StatusOK, created)
}

// handleWebMemoryEdit updates the text and importance of an existing memory.
func (a *App) handleWebMemoryEdit(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeWebErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	id, ok := parseWebMemoryID(w, r)
	if !ok {
		return
	}
	req, ok := parseWebMemoryReq(w, r)
	if !ok {
		return
	}
	var updated webMemory
	var apiErr *webAPIError
	a.submitSync(func() {
		sess, err := a.webAuthSession(token)
		if err != nil {
			apiErr = err
			return
		}
		found := false
		for i := range sess.data.Memories {
			if sess.data.Memories[i].ID == id {
				sess.data.Memories[i].Text = req.Text
				sess.data.Memories[i].Importance = req.Importance
				updated = toWebMemory(sess.data.Memories[i])
				found = true
				break
			}
		}
		if !found {
			apiErr = &webAPIError{http.StatusNotFound, fmt.Sprintf("no memory with id %d", id)}
			return
		}
		if saveErr := a.store.Save(sess.userID, sess.data); saveErr != nil {
			log.Print("web", "save after edit failed for %d: %v", sess.userID, saveErr)
			apiErr = &webAPIError{http.StatusInternalServerError, "couldn't save the memory"}
		}
	})
	if apiErr != nil {
		writeWebErr(w, apiErr.status, apiErr.msg)
		return
	}
	writeWebJSON(w, http.StatusOK, updated)
}

// handleWebMemoryDelete removes a memory by id.
func (a *App) handleWebMemoryDelete(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	if token == "" {
		writeWebErr(w, http.StatusUnauthorized, "missing token")
		return
	}
	id, ok := parseWebMemoryID(w, r)
	if !ok {
		return
	}
	var apiErr *webAPIError
	a.submitSync(func() {
		sess, err := a.webAuthSession(token)
		if err != nil {
			apiErr = err
			return
		}
		if !sess.data.DeleteMemory(id) {
			apiErr = &webAPIError{http.StatusNotFound, fmt.Sprintf("no memory with id %d", id)}
			return
		}
		if saveErr := a.store.Save(sess.userID, sess.data); saveErr != nil {
			log.Print("web", "save after delete failed for %d: %v", sess.userID, saveErr)
			apiErr = &webAPIError{http.StatusInternalServerError, "couldn't save the change"}
		}
	})
	if apiErr != nil {
		writeWebErr(w, apiErr.status, apiErr.msg)
		return
	}
	writeWebJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// parseWebMemoryReq decodes and validates a memory add/edit request body.
func parseWebMemoryReq(w http.ResponseWriter, r *http.Request) (webMemoryReq, bool) {
	var req webMemoryReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeWebErr(w, http.StatusBadRequest, "invalid request body")
		return req, false
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" {
		writeWebErr(w, http.StatusBadRequest, "text is required")
		return req, false
	}
	if req.Importance < 1 || req.Importance > 10 {
		writeWebErr(w, http.StatusBadRequest, "importance must be between 1 and 10")
		return req, false
	}
	return req, true
}

// parseWebMemoryID extracts the {id} path value as a positive integer.
func parseWebMemoryID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id < 1 {
		writeWebErr(w, http.StatusBadRequest, "invalid memory id")
		return 0, false
	}
	return id, true
}

// bearerToken extracts the token from the Authorization header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[len("Bearer "):])
	}
	return ""
}

func toWebMemory(m storage.Memory) webMemory {
	return webMemory{ID: m.ID, Importance: m.Importance, Text: m.Text, Date: m.Date}
}

func toWebMemories(ms []storage.Memory) []webMemory {
	out := make([]webMemory, 0, len(ms))
	for _, m := range ms {
		out = append(out, toWebMemory(m))
	}
	return out
}

func writeWebJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeWebErr(w http.ResponseWriter, status int, msg string) {
	writeWebJSON(w, status, map[string]string{"error": msg})
}

// randomString returns a cryptographically random string of n characters
// from webAlphabet.
func randomString(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = webAlphabet[int(raw[i])%len(webAlphabet)]
	}
	return string(b)
}
