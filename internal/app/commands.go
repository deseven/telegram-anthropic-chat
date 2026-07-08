package app

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/zoo/telegram-anthropic-chat/internal/log"
	"github.com/zoo/telegram-anthropic-chat/internal/memories"
)

// handleCommand checks whether a message is a command (any text starting with
// "/") and, if so, dispatches it to the corresponding command handler. It
// returns true when the message was consumed as a command (and must NOT be
// enqueued into the LLM context), false otherwise.
//
// Any message whose text starts with "/" is treated as a command — even a
// lone "/". Unknown commands produce a short help message listing the
// available commands.
func (a *App) handleCommand(ctx context.Context, chatID int64, userID int64, text string) bool {
	if !strings.HasPrefix(text, "/") {
		return false
	}

	// Whitelist: user data file must exist. Commands from unknown users are
	// rejected the same way ordinary messages are.
	if !a.store.Exists(userID) {
		a.sendText(ctx, chatID, fmt.Sprintf("Access denied. Your user ID is %d.", userID))
		return true
	}

	sess, err := a.getOrCreateSession(userID)
	if err != nil {
		log.Print("app", "session load failed for %d: %v", userID, err)
		a.sendText(ctx, chatID, "Sorry, I couldn't load your data. Please try again later.")
		return true
	}

	// Commands are dispatched onto the global queue so that their data access
	// (and, for /end, the Anthropic API call) is serialized with chat requests
	// and memory extraction. The command sends its own reply from inside the
	// job, so the update handler returns immediately without blocking.
	switch parseCommand(text) {
	case "mem", "memories":
		a.submit(func() { a.cmdMem(context.Background(), chatID, sess, text) })
	case "end":
		a.submit(func() { a.cmdEnd(context.Background(), chatID, sess) })
	case "forget":
		a.submit(func() { a.cmdForget(context.Background(), chatID, sess) })
	case "rld", "reload":
		a.submit(func() { a.cmdReload(context.Background(), chatID, sess) })
	default:
		a.sendText(ctx, chatID, "Unknown command. Available commands:\n/mem — show your memories\n/mem del {id} [{id} ...] — delete one or more memories by id\n/end — end the current session and extract memories\n/forget — end the current session without extracting memories\n/rld — reload your data (description and memories) from disk")
	}
	return true
}

// parseCommand extracts the command name from a message text. A command starts
// with "/" followed by word characters, optionally suffixed with "@BotName".
// Returns the lowercased command name (without the slash or bot suffix), or ""
// if the text is just "/" with no name.
func parseCommand(text string) string {
	// Take the first whitespace-delimited token.
	fields := strings.Fields(text)
	if len(fields) == 0 {
		// text is "/" (or "/" plus whitespace only): no command name.
		return ""
	}
	token := strings.TrimPrefix(fields[0], "/")
	// Strip an optional "@BotName" suffix.
	if i := strings.IndexByte(token, '@'); i >= 0 {
		token = token[:i]
	}
	return strings.ToLower(token)
}

// cmdMem implements the /mem command. With no arguments it lists the user's
// memories, split into the ones currently sent to the LLM context ("Current
// context:") and the remaining ones ("Other memories:"), each prefixed with
// its id. With the "del {id ...}" subcommand it deletes one or more memories
// by id in a single invocation.
//
// The output is never added to the LLM context. The whole command (including
// the delete path) runs on the global queue so its data access is serialized
// with chat requests and memory extraction.
func (a *App) cmdMem(ctx context.Context, chatID int64, sess *session, text string) {
	// Parse optional subcommand. fields[0] is the command token (e.g. "/mem").
	fields := strings.Fields(text)
	if len(fields) >= 2 && strings.EqualFold(fields[1], "del") {
		if len(fields) < 3 {
			a.sendText(ctx, chatID, "Usage: /mem del {id} [{id} ...]")
			return
		}
		// Parse all remaining tokens as memory ids. Every token must be a
		// valid positive integer; if any is invalid we reject the whole
		// request so no partial deletion happens.
		ids := make([]int, 0, len(fields)-2)
		for _, f := range fields[2:] {
			id, err := strconv.Atoi(f)
			if err != nil || id < 1 {
				a.sendText(ctx, chatID, "Invalid memory id. Usage: /mem del {id} [{id} ...]")
				return
			}
			ids = append(ids, id)
		}
		a.cmdMemDel(ctx, chatID, sess, ids)
		return
	}

	in, out := memories.Split(sess.data.Memories, a.cfg.MemoriesCtxSize, sess.data.Sessions)

	var sb strings.Builder
	if len(in) == 0 && len(out) == 0 {
		sb.WriteString("You have no memories yet.")
	} else {
		if r := memories.RenderList(in); r != "" {
			// Two trailing spaces force a hard line break before the list.
			sb.WriteString("**Current context:**  \n")
			sb.WriteString(r)
		}
		if len(out) > 0 {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString("**Other memories:**  \n")
			sb.WriteString(memories.RenderList(out))
		}
	}

	a.sendMarkdown(ctx, chatID, sb.String())
}

// cmdMemDel deletes one or more memories by id and persists the change. It
// must be called from the global queue worker (cmdMem submits it there) so the
// data mutation and Save are serialized with all other work.
//
// Deletion is atomic with respect to persistence: the session is saved only
// once after all requested ids have been processed. Ids that don't match any
// memory are reported back to the user but do not abort the operation.
func (a *App) cmdMemDel(ctx context.Context, chatID int64, sess *session, ids []int) {
	var deleted, missing []int
	for _, id := range ids {
		if sess.data.DeleteMemory(id) {
			deleted = append(deleted, id)
		} else {
			missing = append(missing, id)
		}
	}

	if len(deleted) == 0 {
		// Nothing was deleted; report the first missing id for clarity.
		a.sendText(ctx, chatID, fmt.Sprintf("No memory with id %d.", ids[0]))
		return
	}

	if err := a.store.Save(sess.userID, sess.data); err != nil {
		log.Print("app", "save after delete failed for %d: %v", sess.userID, err)
		a.sendText(ctx, chatID, "Memory found but couldn't be saved. Please try again later.")
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Deleted %d memor%s.", len(deleted), pluralMemory(len(deleted))))
	if len(missing) > 0 {
		sb.WriteString(fmt.Sprintf(" No memory with id %v.", missing))
	}
	a.sendText(ctx, chatID, sb.String())
}

// cmdEnd ends the user's current session: it triggers memory extraction for
// the in-memory conversation, reports the result to the user, and clears the
// session context. The command itself is never added to the LLM context.
func (a *App) cmdEnd(ctx context.Context, chatID int64, sess *session) {
	// Stop the session-timeout timer: the user is ending the session
	// explicitly, so there is no need for a later automatic extraction.
	if sess.timer != nil {
		sess.timer.Stop()
		sess.timer = nil
	}

	a.sendTyping(ctx, chatID)

	added, err := a.extractAndClear(ctx, sess)
	if err != nil {
		a.sendText(ctx, chatID, "Memory extraction failed. Your session was not cleared; please try again later.")
		return
	}

	var msg string
	switch {
	case added == 0:
		msg = "Session ended. No new memories were extracted."
	default:
		msg = fmt.Sprintf("Session ended. %d new memor%s extracted and saved.", added, pluralMemory(added))
	}
	a.sendText(ctx, chatID, msg)
}

// cmdForget ends the user's current session without extracting memories: it
// simply clears the in-memory conversation context. No Anthropic API call is
// made and no session UUID is recorded. The command itself is never added to
// the LLM context.
func (a *App) cmdForget(ctx context.Context, chatID int64, sess *session) {
	// Stop the session-timeout timer: the user is ending the session
	// explicitly, so there is no need for a later automatic extraction.
	if sess.timer != nil {
		sess.timer.Stop()
		sess.timer = nil
	}

	// Clear in-memory context; next message starts a fresh session. No
	// memory extraction, no session UUID, no persistence.
	sess.ctx = nil
	sess.uuid = ""

	a.sendText(ctx, chatID, "Session ended. No memories were extracted.")
}

// pluralMemory returns "y" for 1, "ies" otherwise (to form "memory"/"memories").
func pluralMemory(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// cmdReload re-reads the user's data file from disk and replaces the
// in-memory copy in the session. This is useful when the description or
// memories were edited directly on disk (or by another process) and the
// running bot should pick up the changes without a restart. The in-memory
// conversation context is preserved. The command itself is never added to
// the LLM context.
func (a *App) cmdReload(ctx context.Context, chatID int64, sess *session) {
	ud, err := a.store.Load(sess.userID)
	if err != nil {
		log.Print("app", "reload failed for %d: %v", sess.userID, err)
		a.sendText(ctx, chatID, "Couldn't reload your data. Please try again later.")
		return
	}
	sess.data = ud
	a.sendText(ctx, chatID, "Your data has been reloaded.")
}
