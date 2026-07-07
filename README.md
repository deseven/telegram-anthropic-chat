# telegram-anthropic-chat

A 1-on-1 Telegram chat bot backed by the Anthropic API, with persistent
per-user memories. Conversations feel infinite to the user: there are no
visible sessions. After the user is inactive for `sessionTimeout` seconds, the
current in-memory conversation is sent to the model to extract important
memories, which are then included (selected by importance and recency) in
future contexts.

## Features

- 1-on-1 chat, no visible session markers.
- Per-user JSON data files (`data/{telegram_user_id}.json`) that also act as a
  whitelist.
- Message debouncing.
- Memory extraction on session timeout, on application exit, and on demand.
  Memories are tagged with a session UUID; recent-session memories get
  fresh-context priority, older ones are ranked by importance.
- Web search / URL extraction via Tavily tool calling (optional).
- Image handling: photos over 1024px are resized, re-encoded as JPEG @ 85%,
  base64-encoded and sent to Anthropic. Forwarded messages are attributed to
  their original author; unsupported attachments become text placeholders.
- Replies are converted to Telegram MarkdownV2 and split when too long.
- Polling or webhook update methods.
- Timestamped backups of user data on every write (up to 10 kept).

## Commands

Any message starting with `/` is treated as a command and never enters the LLM
context. Unknown commands print a short help.

- `/mem` — list your memories, split into the ones currently in context and the
  rest, each prefixed with its id.
- `/mem del {id} [{id} ...]` — delete one or more memories by id.
- `/end` — end the current session and extract memories immediately.
- `/rld` — reload your data (description and memories) from disk; the in-memory
  conversation is preserved.

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
| `sessionTimeout`     | no       | `3600`                       | Seconds of inactivity before memory extraction |
| `systemPrompt`       | no       | `prompts/system.md`          | Path to chat system prompt |
| `memoriesPrompt`     | no       | `prompts/memories-system.md` | Path to memory-extraction system prompt |
| `memoriesUserPrompt` | no       | `prompts/memories-user.md`   | Path to memory-extraction user prompt |
| `webhookPort`        | no       | `5666`                       | Webhook HTTP server port |
| `webhookSecretToken` | no       |                              | Telegram webhook secret token |
| `webhookPublicURL`   | no       |                              | Public URL for `SetWebhook` |
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
- [github.com/Mad-Pixels/goldmark-tgmd](https://github.com/Mad-Pixels/goldmark-tgmd) — CommonMark to Telegram MarkdownV2 conversion
- [golang.org/x/image](https://pkg.go.dev/golang.org/x/image)
