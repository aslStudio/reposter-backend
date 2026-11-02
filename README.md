# Reposter backend

The first implemented integration monitors public Telegram channels, rewrites new text posts with Codex, stores images and drafts, then publishes to an owned Telegram channel after approval or automatically. X and Threads are represented in the domain model; their connectors come later.

## Local setup

1. Copy `.env.example` to `.env` and set long random `POSTGRES_PASSWORD` and `ADMIN_TOKEN` values. Use an alphanumeric or hexadecimal database password because Compose inserts it into a database URL.
2. Set `CODEX_API_KEY` to an OpenAI Platform API key. The server runs `codex exec` with the configurable `CODEX_MODEL` (default `gpt-6-luna`). API model usage is billed to that key.
3. Create a Telegram bot with BotFather, set `TELEGRAM_BOT_TOKEN`, and add the bot as an administrator of the destination channel with permission to post. Keep both keys in `.env`, never in requests or Git.
4. Start the app and PostgreSQL: `docker compose up --build -d`. The admin API listens only on `127.0.0.1:8080` on the host.

The app creates its schema on startup. PostgreSQL data and saved source images live in Docker named volumes. No Redis or message broker is needed for the single-process worker.

If `BOOTSTRAP_TG_SOURCE` and `BOOTSTRAP_TG_DESTINATION` are set in `.env`, startup creates or updates that route using `BOOTSTRAP_TG_STYLE`, `BOOTSTRAP_TG_MODE`, `BOOTSTRAP_TG_DAILY_LIMIT`, `BOOTSTRAP_TG_FORMATTING_ENABLED`, and `BOOTSTRAP_TG_SIGNATURE_TEXT`. The local test route is configured this way. Restarting the app keeps the route and updates its settings; no manual API calls are needed for this route.

## Configure a route

All `/v1` requests require the `X-Admin-Token` header. The full HTTP contract is in [`api/openapi.yaml`](api/openapi.yaml).

```sh
curl -X POST http://127.0.0.1:8080/v1/sources \
  -H "X-Admin-Token: $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"url":"https://t.me/example_source"}'

curl -X POST http://127.0.0.1:8080/v1/destinations \
  -H "X-Admin-Token: $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"chat_id":"@my_channel","style_prompt":"Кратко и спокойно, на русском языке","mode":"manual_approval","daily_limit":3,"formatting_enabled":true,"signature_text":"@my_channel"}'

curl -X POST http://127.0.0.1:8080/v1/routes \
  -H "X-Admin-Token: $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"source_id":1,"destination_id":1}'
```

The first poll records the newest visible source post as a baseline, without preparing old posts. Set `REPLAY_LATEST_ON_FIRST_POLL=true` to process only the newest existing post on first poll; this is useful for a test route. New posts are then checked every `POLL_INTERVAL_SECONDS` (minimum 30). Trigger a cycle manually with `POST /v1/poll`. Read drafts with `GET /v1/drafts?status=pending_approval` and approve one with `POST /v1/drafts/{id}/approve`. Approved drafts are sent on the next cycle. With `mode: automatic`, new drafts are eligible immediately. Limits are counted per destination per UTC day; `daily_limit: 0` disables the limit. Set `formatting_enabled: true` to render supported Markdown as Telegram HTML. Set `signature_text` to add a linked footer; the destination must have a public `@username`, and the link always points to that destination. Explicit channel promotion at the end of source posts is removed before rewriting.

## Current boundaries

- This is a local, single-operator admin API. It has no user accounts, subscription billing, frontend, or public network authentication beyond the admin token. Do not expose it directly to the internet.
- Telegram sources are read from public `t.me/s/<channel>` HTML. The reader handles text and image posts; image-only and video posts do not yet create drafts. Source media is stored in a local Docker volume. Source pages, formats, and Telegram conditions may change.
- The worker does not automatically retry uncertain Telegram publication outcomes. A `publishing` or `publish_failed` draft needs inspection before any retry, so an API timeout does not duplicate posts.
- After fixing channel membership, `POST /v1/drafts/{id}/retry` requeues a draft that Telegram definitively rejected with `403 bot is not a member of the channel chat`. Other failures are not eligible for this retry endpoint.
- The bot must be able to publish in the configured destination. Setup does not yet verify its administrator rights.
- Codex rewrites source text with each destination's style prompt. Live API access still needs verification in an environment with outbound network access.

## Development

```sh
go test ./...
go run ./cmd/reposter
```

For `go run`, set `DATABASE_URL`, `ADMIN_TOKEN`, `CODEX_API_KEY`, and `TELEGRAM_BOT_TOKEN` in the process environment. The private product plan is in `docs/product-plan.private.md`, which is ignored by Git.
