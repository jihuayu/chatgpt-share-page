# chatgpt-share-page

Import public ChatGPT share links into self-hosted, immutable conversation
snapshots. The service fetches a `chatgpt.com/share/<id>` page once, extracts
the conversation payload, normalizes it into a stable JSON model, and renders
immutable full-page and embed HTML — so readers never talk to `chatgpt.com`.

Design doc: [docs/design.md](docs/design.md)

```text
ChatGPT Share URL -> fetch -> extract -> normalize
    -> SQLite (metadata, revisions, admin token hashes)
    -> files (raw.json, snapshot.json, page.html, embed.html)
    -> /c/<slug>/r/<rev> and /e/<slug>/r/<rev>
```

## Run

```sh
go run ./cmd/server
```

Open `http://localhost:8080` to import a public Share URL from the browser. The
result view exposes the stable full-page and embed URLs. A management token is
shown only for a newly created snapshot; store it before leaving the page.

Configuration via environment variables:

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | listen port |
| `DATA_DIR` | `./data` | artifact root |
| `DATABASE_PATH` | `./data/app.db` | SQLite file (WAL) |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | base URL for public links |
| `APP_BASE_URL` | `http://localhost:8080` | base URL for the API |
| `MAX_IMPORT_CONCURRENCY` | `2` | concurrent imports |
| `FETCH_TIMEOUT_SECONDS` | `30` | upstream fetch timeout |
| `MAX_FETCH_BYTES` | `20971520` | upstream HTML limit |
| `MAX_REQUEST_BYTES` | `1048576` | API request body limit |
| `MAX_REDIRECTS` | `3` | share-URL redirect limit |
| `CLOUDFLARE_API_TOKEN` | – | optional; enables edge purge |
| `CLOUDFLARE_ZONE_ID` | – | optional; enables edge purge |
| `LOG_LEVEL` | `info` | slog level |

## API

### Import

```http
POST /api/v1/snapshots
{"url": "https://chatgpt.com/share/<id>",
 "title": "optional override", "include_hidden": false,
 "all_nodes": false, "timezone": "Asia/Shanghai", "timeout_seconds": 30}
```

Returns `id`, `slug`, `revision`, `page_url`, `embed_url`, and a one-time
`admin_token` (only its SHA-256 is stored). Re-importing an already imported
share URL returns the existing snapshot with `existing: true`.

### Snapshot lifecycle (require `Authorization: Bearer <admin_token>`)

```http
GET    /api/v1/snapshots/{id}
POST   /api/v1/snapshots/{id}/refresh   # new immutable revision if changed
DELETE /api/v1/snapshots/{id}           # stable URLs stop serving immediately
POST   /api/v1/previews                 # {"snapshot_id","kind":"page|embed","url"?}
```

### Public pages

```text
GET /c/<slug>              -> 302 to active revision
GET /c/<slug>/r/<rev>      -> immutable full page (long-lived cache, ETag)
GET /e/<slug>              -> 302 to active revision
GET /e/<slug>/r/<rev>      -> immutable embed page (iframe-friendly)
GET /healthz               -> {"status":"ok"}
```

Embed a conversation:

```html
<iframe src="https://share.example.com/e/<slug>/r/<rev>"
        title="AI conversation" loading="lazy"
        sandbox="allow-scripts allow-popups"
        style="width:100%;border:0;min-height:480px"></iframe>
```

## Docker

Build and run the service with a named volume for SQLite and generated
artifacts:

```sh
docker build -t chatgpt-share-page .
docker run --rm -p 8080:8080 \
  -v chatgpt-share-data:/data \
  -e PUBLIC_BASE_URL=http://localhost:8080 \
  chatgpt-share-page
```

For a public deployment, set `PUBLIC_BASE_URL` and `APP_BASE_URL` to the
external HTTPS origin. The image runs as a non-root user and writes only under
`/data`.

## Security notes

- Only `https://chatgpt.com/share/...` / `chat.openai.com/share/...` URLs are
  fetched; every redirect is revalidated, and DNS answers pointing at
  non-public IPs are refused.
- Markdown is rendered with Goldmark + Chroma at publish time and sanitized
  with bluemonday (https-only links, no scripts/iframes/forms).
- Public pages ship a strict CSP (`default-src 'none'`, hashed inline
  scripts) and `X-Robots-Tag: noindex` by default.
- Admin tokens are high-entropy, returned once, and stored as SHA-256 hashes.

## Development

```sh
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/server
```
