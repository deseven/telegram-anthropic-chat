// Package app orchestrates the bot: per-user sessions, in-memory chat context,
// message handling, and memory extraction on session timeout / application exit.
package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/google/uuid"

	"github.com/zoo/telegram-anthropic-chat/internal/config"
	"github.com/zoo/telegram-anthropic-chat/internal/images"
	"github.com/zoo/telegram-anthropic-chat/internal/llm"
	"github.com/zoo/telegram-anthropic-chat/internal/log"
	"github.com/zoo/telegram-anthropic-chat/internal/markdown"
	"github.com/zoo/telegram-anthropic-chat/internal/memories"
	"github.com/zoo/telegram-anthropic-chat/internal/prompt"
	"github.com/zoo/telegram-anthropic-chat/internal/storage"
	"github.com/zoo/telegram-anthropic-chat/internal/tavily"
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

// Tunable timing constants.
const (
	// messageDebounce is how long we wait after the last incoming user message
	// before treating the batch as complete and sending the LLM request. This
	// lets a user forward several messages (e.g. a forwarded message + their
	// comment) and have them combined into a single user turn.
	messageDebounce = 1 * time.Second

	// typingInterval is how often we re-send the "typing" chat action to
	// Telegram while waiting for the LLM response. Telegram's typing indicator
	// expires after ~5 seconds, so we refresh it every second.
	typingInterval = 1 * time.Second
)

// App holds all shared dependencies and per-user session state.
type App struct {
	cfg     *config.Config
	store   *storage.Store
	llm     *llm.Client
	tgbot   *bot.Bot
	tavily  *tavily.Client // nil when tavilyApiKey is not configured

	sysPromptTmpl          string
	memoriesPromptTmpl     string
	memoriesUserPromptTmpl string

	mu       sync.Mutex
	sessions map[int64]*session

	// jobCh is the global, single-worker job queue. Every operation that
	// touches user data or the Anthropic API is submitted here and executed
	// one at a time by worker(). This serializes all such work across all
	// users, eliminating data races and smoothing Anthropic API rate-limit
	// pressure.
	jobCh chan job
}

type session struct {
	userID int64
	data   *storage.UserData
	ctx    []llm.Message // in-memory conversation context (current session)
	uuid   string        // UUID of the current chat session; assigned on first message, used to tag extracted memories

	// sessionStart is the time captured when the session began. It is used
	// to substitute {now} in prompts so the value stays constant across all
	// turns of the session, avoiding prompt-cache invalidation that would
	// occur if the time changed on every request.
	sessionStart time.Time

	// inbox collects incoming messages until the debounce fires.
	inbox []incomingMsg

	// debounceTimer fires messageDebounce after the last incoming message,
	// triggering a single combined LLM request for the whole batch.
	debounceTimer *time.Timer

	// mu protects inbox and debounceTimer from concurrent access between the
	// Telegram update handler (which appends to inbox) and the global queue
	// worker (which drains it). All other session state (ctx, data, timer) is
	// only ever touched by the single global worker goroutine, so it needs no
	// separate lock.
	mu sync.Mutex

	timer *time.Timer // session-timeout memory extraction timer
}

// incomingMsg is a Telegram message awaiting debounce, paired with its chat ID.
type incomingMsg struct {
	chatID int64
	msg    *models.Message
}

// New constructs an App. Prompt templates are loaded and validated here.
// tvClient may be nil when web search is not configured; in that case no
// tools are offered to the model.
func New(cfg *config.Config, store *storage.Store, llmClient *llm.Client, tgbot *bot.Bot, tvClient *tavily.Client) (*App, error) {
	sysTmpl, err := prompt.Load(cfg.SystemPrompt)
	if err != nil {
		return nil, err
	}
	memTmpl, err := prompt.Load(cfg.MemoriesPrompt)
	if err != nil {
		return nil, err
	}
	memUserTmpl, err := prompt.Load(cfg.MemoriesUserPrompt)
	if err != nil {
		return nil, err
	}
	a := &App{
		cfg:                    cfg,
		store:                  store,
		llm:                    llmClient,
		tgbot:                  tgbot,
		tavily:                 tvClient,
		sysPromptTmpl:          sysTmpl,
		memoriesPromptTmpl:     memTmpl,
		memoriesUserPromptTmpl: memUserTmpl,
		sessions:               make(map[int64]*session),
		jobCh:                  make(chan job, 256),
	}
	if tvClient != nil {
		log.Print("app", "web search (Tavily) enabled")
	}
	go a.worker()
	return a, nil
}

// job is a unit of work executed sequentially by the global queue worker.
type job func()

// worker drains the job channel and runs jobs one at a time. Anything that
// touches user data or the Anthropic API is submitted here so that no two such
// operations ever run concurrently — eliminating data races and smoothing
// Anthropic API rate-limit pressure.
func (a *App) worker() {
	for j := range a.jobCh {
		j()
	}
}

// submit enqueues a job for asynchronous execution on the global queue worker.
func (a *App) submit(j job) {
	a.jobCh <- j
}

// submitSync enqueues a job and blocks the caller until it has been executed.
func (a *App) submitSync(j job) {
	done := make(chan struct{})
	a.jobCh <- func() {
		j()
		close(done)
	}
	<-done
}

// Handler is the default Telegram update handler.
func (a *App) Handler(ctx context.Context, b *bot.Bot, update *models.Update) {
	if update.Message == nil {
		return
	}
	userID := update.Message.From.ID
	chatID := update.Message.Chat.ID

	// Commands (any text starting with "/") are handled out-of-band: they
	// never enter the LLM context.
	if a.handleCommand(ctx, chatID, userID, update.Message.Text) {
		return
	}

	// Whitelist: user data file must exist.
	if !a.store.Exists(userID) {
		a.sendText(ctx, chatID, fmt.Sprintf("Access denied. Your user ID is %d.", userID))
		return
	}

	sess, err := a.getOrCreateSession(userID)
	if err != nil {
		log.Print("app", "session load failed for %d: %v", userID, err)
		a.sendText(ctx, chatID, "Sorry, I couldn't load your data. Please try again later.")
		return
	}

	a.enqueue(ctx, chatID, sess, update.Message)
}

func (a *App) getOrCreateSession(userID int64) (*session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if s, ok := a.sessions[userID]; ok {
		return s, nil
	}

	ud, err := a.store.Load(userID)
	if err != nil {
		return nil, err
	}

	s := &session{
		userID: userID,
		data:   ud,
	}
	a.sessions[userID] = s
	return s, nil
}

// enqueue adds an incoming message to the session's inbox and (re)schedules the
// debounce timer. When the timer fires, all accumulated messages are combined
// into a single user turn and sent to the LLM.
func (a *App) enqueue(ctx context.Context, chatID int64, s *session, msg *models.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	log.Print("app", "incoming message from user %d", s.userID)

	s.inbox = append(s.inbox, incomingMsg{chatID: chatID, msg: msg})

	// (Re)schedule the debounce timer: every new message resets the wait so we
	// batch a rapid burst of messages into one user turn. When it fires, the
	// actual processing is submitted to the global queue so it runs
	// sequentially with all other data/API work.
	if s.debounceTimer != nil {
		s.debounceTimer.Stop()
	}
	s.debounceTimer = time.AfterFunc(messageDebounce, func() {
		a.submit(func() { a.flushInbox(s) })
	})
}

// flushInbox drains the session's inbox and processes the batched messages.
// It runs on the global queue worker goroutine.
func (a *App) flushInbox(s *session) {
	s.mu.Lock()
	if s.debounceTimer != nil {
		s.debounceTimer.Stop()
		s.debounceTimer = nil
	}
	if len(s.inbox) == 0 {
		s.mu.Unlock()
		return
	}
	batch := s.inbox
	s.inbox = nil
	s.mu.Unlock()

	a.handleBatch(batch, s)
}

// handleBatch processes a debounced batch of messages: it converts each message
// to content blocks, combines them into a single user turn (separated by ---),
// appends it to the context, and requests an assistant reply.
func (a *App) handleBatch(batch []incomingMsg, s *session) {
	a.resetTimer(s)

	chatID := batch[0].chatID

	// A new session starts when the in-memory context is empty: assign a
	// fresh UUID so memories extracted from this session can be tagged, and
	// capture the session start time. The start time is reused for {now}
	// substitutions across all turns of the session so the system prompt
	// stays byte-identical (preserving prompt-cache hits).
	if len(s.ctx) == 0 && s.uuid == "" {
		s.uuid = uuid.NewString()
		s.sessionStart = time.Now()
	}
	now := s.sessionStart
	if now.IsZero() {
		// Defensive: should not happen, but never pass a zero time to Render.
		now = time.Now()
	}

	// Build content blocks for each message, joining individual messages with a
	// "---" separator so the model can distinguish them.
	var blocks []llm.ContentBlock
	for idx, im := range batch {
		msgBlocks, err := a.extractContent(context.Background(), im.msg)
		if err != nil {
			log.Print("app", "extract content for %d: %v", s.userID, err)
			a.sendText(context.Background(), im.chatID, "I couldn't process one of the attachments.")
			continue
		}
		if len(msgBlocks) == 0 {
			continue
		}
		if len(blocks) > 0 {
			blocks = append(blocks, llm.ContentBlock{Text: "\n---\n"})
		}
		blocks = append(blocks, msgBlocks...)
		_ = idx
	}
	if len(blocks) == 0 {
		return
	}

	// Append the combined user message to in-memory context.
	s.ctx = append(s.ctx, llm.Message{Role: "user", Blocks: blocks})

	// Build system prompt with current memories. `now` was captured at
	// session start (see above) to keep the prompt stable across turns.
	memList := memories.Select(s.data.Memories, a.cfg.MemoriesCtxSize, s.data.Sessions)
	system := prompt.Render(a.sysPromptTmpl, s.data.UserDescription, memList, now)

	// Send typing notifications every typingInterval until the LLM responds.
	typingCtx, typingCancel := context.WithCancel(context.Background())
	defer typingCancel()
	go a.typingLoop(typingCtx, chatID)

	log.Print("app", "sending LLM request for user %d (%d context messages)", s.userID, len(s.ctx))

	// When web search is enabled, the model may emit intermediate text
	// (e.g. "Let me look that up") before making a tool call. We forward
	// such text to the user immediately via the onText callback while keeping
	// the typing indicator alive for the rest of the tool-calling chain. The
	// final (non-tool) turn's text is returned as the reply and sent once
	// below — it is never forwarded via the callback, so there is no
	// duplication.
	//
	// ChatWithTools appends every turn (assistant text + tool_use, user
	// tool_result, and the final assistant text) to s.ctx, so the in-memory
	// context is the source of truth and reflects the standard tool-calling
	// flow the model expects in subsequent turns.
	onText := func(text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		a.sendMarkdown(context.Background(), chatID, text)
	}

	reply, err := a.llm.ChatWithTools(context.Background(), system, &s.ctx, a.tavily, onText)
	typingCancel()
	if err != nil {
		log.Print("app", "LLM request failed for %d: %v", s.userID, err)
		s.ctx = s.ctx[:len(s.ctx)-1] // roll back the unanswered user message
		a.sendText(context.Background(), chatID, "I ran into an error while thinking. Please try again.")
		return
	}
	log.Print("app", "received LLM response for user %d (%d chars)", s.userID, len(reply))

	if reply != "" {
		a.sendMarkdown(context.Background(), chatID, reply)
	}
}

// typingLoop sends the "typing" chat action to Telegram every typingInterval
// until ctx is cancelled (i.e. the LLM response arrives or the request fails).
func (a *App) typingLoop(ctx context.Context, chatID int64) {
	ticker := time.NewTicker(typingInterval)
	defer ticker.Stop()
	// Send immediately so the indicator appears without waiting for the first tick.
	a.sendTyping(ctx, chatID)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.sendTyping(ctx, chatID)
		}
	}
}

func (a *App) sendTyping(ctx context.Context, chatID int64) {
	_, err := a.tgbot.SendChatAction(ctx, &bot.SendChatActionParams{
		ChatID: chatID,
		Action: models.ChatActionTyping,
	})
	if err != nil {
		log.Print("app", "send typing to %d failed: %v", chatID, err)
	}
}

// extractContent converts a Telegram message into LLM content blocks.
func (a *App) extractContent(ctx context.Context, msg *models.Message) ([]llm.ContentBlock, error) {
	var blocks []llm.ContentBlock

	// Forwarded messages: prefix the content with the original author so the
	// model can attribute quotes correctly instead of treating them as the
	// user's own words.
	if author := forwardAuthor(msg); author != "" {
		blocks = append(blocks, llm.ContentBlock{Text: "[Forwarded message from " + author + "]"})
	}

	// Photos
	if len(msg.Photo) > 0 {
		// Pick the largest photo.
		ph := msg.Photo[len(msg.Photo)-1]
		raw, err := a.downloadFile(ctx, ph.FileID)
		if err != nil {
			return nil, fmt.Errorf("download photo: %w", err)
		}
		b64, err := images.Process(raw)
		if err != nil {
			return nil, fmt.Errorf("process photo: %w", err)
		}
		blocks = append(blocks, llm.ContentBlock{ImageB64: b64})
	}

	// Caption (text accompanying photos)
	if msg.Caption != "" {
		blocks = append(blocks, llm.ContentBlock{Text: msg.Caption})
	}

	// Plain text
	if msg.Text != "" {
		blocks = append(blocks, llm.ContentBlock{Text: msg.Text})
	}

	// Unsupported attachments (document, video, voice, etc.)
	var unsupported []string
	if msg.Document != nil {
		unsupported = append(unsupported, msg.Document.FileName)
	}
	if msg.Video != nil {
		unsupported = append(unsupported, "video")
	}
	if msg.Voice != nil {
		unsupported = append(unsupported, "voice")
	}
	if msg.Audio != nil {
		unsupported = append(unsupported, "audio")
	}
	if msg.Sticker != nil {
		unsupported = append(unsupported, "sticker")
	}
	if len(unsupported) > 0 {
		text := fmt.Sprintf("[User attached following files: %s. This file type is not supported and you cannot read it.]", strings.Join(unsupported, ", "))
		blocks = append(blocks, llm.ContentBlock{Text: text})
	}

	return blocks, nil
}

// forwardAuthor returns a human-readable name for the original author of a
// forwarded message, or an empty string if the message is not forwarded (or
// the author cannot be determined). It covers all MessageOrigin variants:
// user, hidden_user, chat, and channel.
func forwardAuthor(msg *models.Message) string {
	origin := msg.ForwardOrigin
	if origin == nil {
		return ""
	}
	switch origin.Type {
	case models.MessageOriginTypeUser:
		if u := origin.MessageOriginUser; u != nil {
			return userDisplayName(u.SenderUser)
		}
	case models.MessageOriginTypeHiddenUser:
		if h := origin.MessageOriginHiddenUser; h != nil {
			return h.SenderUserName
		}
	case models.MessageOriginTypeChat:
		if c := origin.MessageOriginChat; c != nil {
			name := c.SenderChat.Title
			if c.AuthorSignature != nil && *c.AuthorSignature != "" {
				if name != "" {
					name += " (" + *c.AuthorSignature + ")"
				} else {
					name = *c.AuthorSignature
				}
			}
			return name
		}
	case models.MessageOriginTypeChannel:
		if c := origin.MessageOriginChannel; c != nil {
			name := c.Chat.Title
			if c.AuthorSignature != nil && *c.AuthorSignature != "" {
				if name != "" {
					name += " (" + *c.AuthorSignature + ")"
				} else {
					name = *c.AuthorSignature
				}
			}
			return name
		}
	}
	return ""
}

// userDisplayName builds a display name from a Telegram User, preferring the
// first/last name combination and falling back to the username.
func userDisplayName(u models.User) string {
	var name string
	if u.FirstName != "" {
		name = u.FirstName
	}
	if u.LastName != "" {
		if name != "" {
			name += " " + u.LastName
		} else {
			name = u.LastName
		}
	}
	if name == "" && u.Username != "" {
		name = "@" + u.Username
	}
	return name
}

// resetTimer (re)schedules the session-timeout memory extraction. The
// extraction itself is submitted to the global queue so it never runs
// concurrently with other data/API work.
func (a *App) resetTimer(s *session) {
	if s.timer != nil {
		s.timer.Stop()
	}
	timeout := time.Duration(a.cfg.SessionTimeout) * time.Second
	s.timer = time.AfterFunc(timeout, func() {
		a.submit(func() { _, _ = a.extractAndClear(context.Background(), s) })
	})
}

// extractAndClear runs the memory extraction utility request and, on success,
// persists updated memories and clears the in-memory context. It returns the
// number of new memories extracted and any error that occurred.
//
// extractAndClear must be called from the global queue worker (or via
// submitSync) so that its data mutations and Anthropic API call are serialized
// with all other work.
func (a *App) extractAndClear(ctx context.Context, s *session) (int, error) {
	ctxMsgs := make([]llm.Message, len(s.ctx))
	copy(ctxMsgs, s.ctx)
	data := s.data

	if len(ctxMsgs) == 0 {
		return 0, nil
	}

	// Ensure the session has a UUID (it is normally assigned on the first
	// message, but guard against any edge case).
	if s.uuid == "" {
		s.uuid = uuid.NewString()
	}
	sessionUUID := s.uuid

	log.Print("app", "extracting memories for user %d (%d messages)", s.userID, len(ctxMsgs))

	// Reuse the session start time for {now} so the memory-extraction
	// prompt is consistent with the conversation that produced it.
	now := s.sessionStart
	if now.IsZero() {
		now = time.Now()
	}
	memList := memories.Select(data.Memories, a.cfg.MemoriesCtxSize, data.Sessions)
	history := llm.SerializeHistory(ctxMsgs)
	system := prompt.Render(a.memoriesPromptTmpl, data.UserDescription, memList, now)
	user := prompt.RenderWithHistory(a.memoriesUserPromptTmpl, data.UserDescription, memList, history, now)

	log.Print("app", "sending memory-extraction LLM request for user %d", s.userID)
	extracted, err := a.llm.ExtractMemoriesFromText(ctx, system, user)
	if err != nil {
		log.Print("app", "memory extraction failed for %d: %v", s.userID, err)
		return 0, err
	}
	log.Print("app", "received memory-extraction response for user %d", s.userID)

	added := 0
	if len(extracted) == 0 {
		log.Print("app", "no new memories for user %d", s.userID)
	} else {
		converted := make([]storage.Memory, 0, len(extracted))
		for _, m := range extracted {
			converted = append(converted, storage.Memory{Importance: m.Importance, Text: m.Text})
		}
		data.AddMemories(converted, sessionUUID)
		data.AddSession(sessionUUID)
		added = len(converted)
		log.Print("app", "added %d memories for user %d (session %s)", added, s.userID, sessionUUID)
	}

	if err := a.store.Save(s.userID, data); err != nil {
		log.Print("app", "save user data failed for %d: %v", s.userID, err)
	}

	// Clear in-memory context; next message starts a fresh session.
	s.ctx = nil
	s.uuid = ""
	s.sessionStart = time.Time{}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	return added, nil
}

// FlushAll extracts memories for all active sessions. Called on application
// exit. Each extraction is submitted synchronously to the global queue so that
// it is serialized with any in-flight work and all memories are persisted
// before the process exits.
func (a *App) FlushAll(ctx context.Context) {
	a.mu.Lock()
	sessions := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		sessions = append(sessions, s)
	}
	a.mu.Unlock()

	log.Print("app", "FlushAll: %d active session(s)", len(sessions))

	for _, s := range sessions {
		if s.timer != nil {
			s.timer.Stop()
		}
		// submitSync runs the job on the global queue worker and blocks until
		// it finishes, guaranteeing the extraction (and its Save) completes
		// before we move on / exit.
		a.submitSync(func() {
			if len(s.ctx) == 0 {
				log.Print("app", "FlushAll: skipping user %d (no messages)", s.userID)
				return
			}
			log.Print("app", "FlushAll: extracting memories for user %d (%d messages)", s.userID, len(s.ctx))
			_, _ = a.extractAndClear(ctx, s)
		})
	}
	log.Print("app", "FlushAll: done")
}

// sendText sends a plain (unparsed) text message. If the text is too long for a
// single Telegram message (4096 chars), it is split into multiple chunks that
// are sent sequentially so the user still receives the whole text.
func (a *App) sendText(ctx context.Context, chatID int64, text string) {
	chunks := markdown.SplitPlainText(text)
	if len(chunks) == 0 {
		chunks = []string{text}
	}
	for _, chunk := range chunks {
		_, err := a.tgbot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   chunk,
		})
		if err != nil {
			log.Print("app", "send message to %d failed: %v", chatID, err)
			return
		}
	}
}

// sendMarkdown sends a message parsed as Telegram MarkdownV2. The text is
// converted from CommonMark to MarkdownV2 first. If the text is too long for a
// single Telegram message (4096 chars), it is split into multiple chunks that
// are sent sequentially. If sending any chunk fails, the remaining chunks fall
// back to plain text so the user still gets the whole reply.
func (a *App) sendMarkdown(ctx context.Context, chatID int64, text string) {
	chunks := markdown.SplitMarkdown(text)
	if len(chunks) == 0 {
		chunks = []string{text}
	}
	for i, chunk := range chunks {
		converted := markdown.ToMarkdownV2(chunk)
		_, err := a.tgbot.SendMessage(ctx, &bot.SendMessageParams{
			ChatID:    chatID,
			Text:      converted,
			ParseMode: models.ParseModeMarkdown,
		})
		if err != nil {
			log.Print("app", "send markdown to %d failed (chunk %d/%d): %v, retrying as plain text", chatID, i+1, len(chunks), err)
			// Fall back to plain text for this and all remaining chunks so
			// nothing is lost.
			a.sendText(ctx, chatID, chunk)
			for _, rest := range chunks[i+1:] {
				a.sendText(ctx, chatID, rest)
			}
			return
		}
	}
}

// downloadFile fetches a Telegram file by file_id via getFile + the file URL.
func (a *App) downloadFile(ctx context.Context, fileID string) ([]byte, error) {
	f, err := a.tgbot.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return nil, fmt.Errorf("getFile: %w", err)
	}
	link := a.tgbot.FileDownloadLink(f)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download file: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download file: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
