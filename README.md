# Reposter backend

The worker monitors public Telegram channels and X accounts, rewrites new text posts with Codex, saves photos and drafts, and publishes to owned Telegram channels or X accounts after approval or automatically. Threads remains planned.

## Local setup

1. Copy `.env.example` to `.env` and set long random `POSTGRES_PASSWORD` and `ADMIN_TOKEN` values. Use an alphanumeric or hexadecimal database password because Compose inserts it into a database URL.
2. Set `CODEX_API_KEY` to an OpenAI Platform API key. The server runs `codex exec` with the configurable `CODEX_MODEL` (default `gpt-6-luna`). API model usage is billed to that key.
3. Create a Telegram bot with BotFather, set `TELEGRAM_BOT_TOKEN`, and add the bot as an administrator of the destination channel with permission to post. Keep both keys in `.env`, never in requests or Git.
4. For X, set `X_BEARER_TOKEN`, `X_CONSUMER_KEY`, and `X_SECRET_KEY`. The local `.env` also contains a random `X_TOKEN_ENCRYPTION_KEY`; keep it across restarts or saved X connections cannot be decrypted. In your X developer app, enable OAuth 1.0a user authentication, grant Read and Write permission, and configure a callback URL (X requires it even for PIN authorization). The destination account does not need its own developer app.
5. Start the app and PostgreSQL: `docker compose up --build -d`. The admin API listens only on `127.0.0.1:8080` on the host.

The app creates its schema on startup. PostgreSQL data and saved source images live in Docker named volumes. No Redis or message broker is needed for the single-process worker.

If `BOOTSTRAP_TG_SOURCE` and `BOOTSTRAP_TG_DESTINATION` are set in `.env`, startup creates or updates that route using `BOOTSTRAP_TG_STYLE`, `BOOTSTRAP_TG_LANGUAGE`, `BOOTSTRAP_TG_MODE`, `BOOTSTRAP_TG_DAILY_LIMIT`, `BOOTSTRAP_TG_FORMATTING_ENABLED`, and `BOOTSTRAP_TG_SIGNATURE_TEXT`. The local test route is configured this way. Restarting the app keeps the route and updates its settings; no manual API calls are needed for this route.

Compose also bootstraps the X source `vastahov_tg` to `@aslDigitalVlad`, with a punchy news style, automatic publication, and no daily publication limit. Override `BOOTSTRAP_X_*` settings, including `BOOTSTRAP_X_LANGUAGE`, in `.env` as needed. The first poll establishes a baseline and does not repost old X content. X sources are polled at most once every five minutes; user IDs are cached to avoid a paid lookup on every poll. Ready X drafts wait until user publication tokens are configured.

### Connect the test X account through Postman

Import [`postman/x-local-connection.postman_collection.json`](postman/x-local-connection.postman_collection.json) into Postman. Set `admin_token` and the X `destination_id` in a private local Postman environment; obtain the ID from the collection's **List destinations** request. The collection already uses `http://127.0.0.1:8080` as `base_url`.

1. Send **Start X connection**. Open the returned `authorization_url` in a browser signed in as `@aslDigitalVlad` and approve the app. X will show a numeric PIN. The response's `request_token` is also saved as a temporary collection variable.
2. Put the PIN into your private Postman environment as `pin`, then send **Complete X connection with PIN** within ten minutes. The response should say `connected` and show `aslDigitalVlad`. The service encrypts the access token and secret in PostgreSQL; neither is returned to Postman.
3. Send **Publish X test post** to publish exactly the text shown in that request. It returns the public post URL. This request bypasses the rewrite pipeline, so edit its text before sending if desired.

For future customers, use a browser OAuth callback flow instead of PIN entry. This backend already stores a separate X connection per destination, but it remains a single-operator local admin API without user authentication or subscription billing.

## Configure a route

All `/v1` requests require the `X-Admin-Token` header. The full HTTP contract is in [`api/openapi.yaml`](api/openapi.yaml).

```sh
curl -X POST http://127.0.0.1:8080/v1/sources \
  -H "X-Admin-Token: $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"url":"https://t.me/example_source"}'

curl -X POST http://127.0.0.1:8080/v1/destinations \
  -H "X-Admin-Token: $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"chat_id":"@my_channel","style_prompt":"Кратко и спокойно","language":"ru","mode":"manual_approval","daily_limit":3,"formatting_enabled":true,"signature_text":"@my_channel"}'

curl -X POST http://127.0.0.1:8080/v1/routes \
  -H "X-Admin-Token: $ADMIN_TOKEN" -H 'Content-Type: application/json' \
  -d '{"source_id":1,"destination_id":1}'
```

Routes can connect a Telegram or X source to either a Telegram or X destination. Create a source and destination, then use their IDs with `POST /v1/routes`. The destination's `language` selects the rewrite language using a BCP 47 tag such as `ru`, `en`, or `tr`. New destinations default to `ru`. Omitting `language` when updating an existing destination keeps its current value; a nonempty `BOOTSTRAP_*_LANGUAGE` explicitly sets it at startup.

The first poll records the newest visible source post as a baseline, without preparing old posts. For Telegram only, set `REPLAY_LATEST_ON_FIRST_POLL=true` to process the newest existing post on first poll; this is useful for a test route. New Telegram posts are checked every `POLL_INTERVAL_SECONDS` (minimum 30). Trigger a cycle manually with `POST /v1/poll`. Read drafts with `GET /v1/drafts?status=pending_approval` and approve one with `POST /v1/drafts/{id}/approve`. Approved drafts are sent on the next cycle. With `mode: automatic`, new drafts are eligible immediately. Limits are counted per destination per UTC day; `daily_limit: 0` disables the limit. Set `formatting_enabled: true` to render supported Markdown as Telegram HTML. Set `signature_text` to add a linked footer; the destination must have a public `@username`, and the link always points to that destination. Explicit channel promotion at the end of source posts is removed before rewriting. For X API setup, `POST /v1/sources` accepts `{"platform":"x","url":"https://x.com/vastahov_tg"}` and `POST /v1/destinations` accepts `{"platform":"x","chat_id":"@aslDigitalVlad",...}`.

## Current boundaries

- This is a local, single-operator admin API. It has no user accounts, subscription billing, frontend, or public network authentication beyond the admin token. Do not expose it directly to the internet.
- Telegram sources are read from public `t.me/s/<channel>` HTML. The reader handles text, images, and MP4 videos exposed in the public page, including media-only posts. Source media is stored in a local Docker volume. Videos are limited to 50 MiB. Telegram publishes one video with `sendVideo` or mixed photo/video albums with `sendMediaGroup`. Source pages, formats, and Telegram conditions may change.
- X sources use the paid X API, not web scraping. Each read post and user lookup can consume credits. The reader uses at most three pages of ten posts per poll, excludes replies and reposts, and saves photos and downloadable MP4 video variants. A backlog beyond three pages needs operator attention before the cursor can advance. X posts accept one video or up to four photos; video upload is chunked and processing is checked before publication. X post text is limited to 280 Unicode characters locally; X applies its own weighted length rules when publishing.
- The worker does not automatically retry uncertain Telegram publication outcomes. A `publishing` or `publish_failed` draft needs inspection before any retry, so an API timeout does not duplicate posts.
- After fixing channel membership or X OAuth1 app permissions, `POST /v1/drafts/{id}/retry` requeues a draft that the platform definitively rejected with the corresponding 403 and no external post ID. Other failures are not eligible for this retry endpoint. An X permission rejection disables automatic publication for that connection until the account is reauthorized; new drafts wait in the queue.
- The bot must be able to publish in the configured destination. Setup does not yet verify its administrator rights.
- Codex rewrites source text with each destination's style prompt. Live API access still needs verification in an environment with outbound network access.

## Development

```sh
go test ./...
go run ./cmd/reposter
```

For `go run`, set `DATABASE_URL`, `ADMIN_TOKEN`, `CODEX_API_KEY`, and `TELEGRAM_BOT_TOKEN` in the process environment. The private product plan is in `docs/product-plan.private.md`, which is ignored by Git.
