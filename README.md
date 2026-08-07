# telegram-anthropic-chat

A 1-on-1 Telegram chat bot backed by the Anthropic API, with persistent
per-user memories. Conversations feel infinite to the user: there are no
visible sessions. After the user is inactive for `sessionTimeout` seconds, the
current in-memory conversation is sent to the model to extract important
memories, which are then included (selected by recency and importance) in
future contexts.

## Features

- 1-on-1 chat, no visible session markers.
- Per-user JSON data files (`data/{telegram_user_id}.json`) that also act as a
  whitelist.
- Message debouncing.
- Memory extraction on session timeout, on application exit, and on demand.
  Memories from the most recent active day always get fresh-context priority;
  the remaining budget is filled with older memories ranked by importance
  (and recency within the same importance).
- Web search / URL extraction via Tavily tool calling (optional).
- Image handling: photos over 1024px are resized, re-encoded as JPEG @ 85%,
  base64-encoded and sent to Anthropic. Forwarded messages are attributed to
  their original author; unsupported attachments become text placeholders.
- Replies are converted to Telegram MarkdownV2 and split when too long.
- Polling or webhook update methods.
- Web interface for managing memories in the browser (add / edit / delete),
  served by the built-in HTTP server; see `/web`.
- Timestamped backups of user data on every write (up to 10 kept).

## Commands

Any message starting with `/` is treated as a command and never enters the LLM
context. Unknown commands print a short help.

- `/web` — get a personal link (valid for 60 seconds) to the web interface for
  managing your memories in the browser. Requires `httpPublicURL`.
- `/end` — end the current session and extract memories immediately.
- `/forget` — end the current session without extracting memories.
- `/rld` — reload your data (description and memories) from disk; the in-memory
  conversation is preserved.


## Web interface

The built-in HTTP server (used for the Telegram webhook in webhook mode) also
serves a single-page web app at `{httpPublicURL}/` for viewing and editing
memories. Send `/web` to the bot to get a link with a temporary 16-symbol auth
code (valid for 60 seconds); the app exchanges it for a permanent token stored
in the browser's localStorage. Tokens are persisted in the user data file
(`web_tokens`) so they survive restarts. Changes made in the web interface are
saved immediately and apply to the next LLM request, whether or not a chat
session is active. The interface is unavailable when `httpPublicURL` is empty.

In webhook mode Telegram calls `{httpPublicURL}/webhook` (registered via
`SetWebhook` on every startup).

## Configuration

Copy `config.jsonc.example` to `config.jsonc` and fill it in.

| Field                | Required | Default                      | Description |
|----------------------|----------|------------------------------|-------------|
| `apiKey`             | yes      |                              | Anthropic API key |
| `botToken`           | yes      |                              | Telegram bot token |
| `botUpdateMethod`    | no       | `polling`                    | `polling` or `webhook` |
| `model`              | no       | `claude-sonnet-5`            | Anthropic model |
| `maxTokens`          | no       | `16384`                      | Max tokens for chat responses |
| `memoriesCtxSize`    | no       | `16384`                      | Character budget for memories in context |
| `memoriesMaxAge`     | no       | `864000`                     | Retention unit (seconds) per importance point: an out-of-context memory is pruned at session end once it hasn't reached the context for `importance × memoriesMaxAge` (default 10 days per point, so importance 1 = 10 days, importance 10 = 100 days) |
| `sessionTimeout`     | no       | `3600`                       | Seconds of inactivity before memory extraction |
| `systemPrompt`       | no       | `prompts/system.md`          | Path to chat system prompt |
| `memoriesPrompt`     | no       | `prompts/memories-system.md` | Path to memory-extraction system prompt |
| `memoriesUserPrompt` | no       | `prompts/memories-user.md`   | Path to memory-extraction user prompt |
| `httpPort`           | no       | `5666`                       | HTTP server port (webhook + web interface) |
| `httpPublicURL`      | webhook mode |                          | Public HTTPS base URL; web app at `/`, webhook at `/webhook` |
| `webhookSecretToken` | no       |                              | Telegram webhook secret token |
| `dumpRequestsPath`   | no       |                              | File to dump Anthropic requests/responses (truncated on start) |
| `tavilyApiKey`       | no       |                              | Tavily API key; enables `web_search` and `extract_url` tools |

## Adding a user

Copy `data/123456789.json.example` to `data/{their_telegram_user_id}.json`
and fill in `user_description`. Only users with a data file can chat.

## Prompt variables

The chat system prompt and the memory-extraction prompts support:
- `{user_description}` — from the user data file
- `{now}` — current datetime
- `{memories}` — rendered memory list

The memory-extraction user prompt additionally supports:
- `{history}` — the session conversation as a JSON array of `{role, content}`
  objects, with images as `[image]` placeholders and tool calls summarized.

## Running

```bash
go build -o bin/tgbot .
./bin/tgbot -config config.jsonc
```

Or with Docker (see `Dockerfile`, `docker-compose.yml.example` and `run.sh`):

## Dependencies

- [github.com/anthropics/anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go)
- [github.com/go-telegram/bot](https://github.com/go-telegram/bot)
- [github.com/google/uuid](https://github.com/google/uuid)
- [github.com/iamwavecut/go-tavily](https://github.com/iamwavecut/go-tavily) — Tavily web-search/extract client
- [github.com/Mad-Pixels/goldmark-tgmd](https://github.com/Mad-Pixels/goldmark-tgmd) — CommonMark to Telegram MarkdownV2 conversion (vendored as a local fork in `third_party/goldmark-tgmd` with proper ordered-list numbering and soft-line-break handling; see the `replace` directive in `go.mod`)
- [golang.org/x/image](https://pkg.go.dev/golang.org/x/image)
