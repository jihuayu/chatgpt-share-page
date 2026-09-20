# ChatGPT Share Page

<p align="center">
  <img src="web/static/logo.png" alt="ChatGPT Share Page logo" width="160">
</p>

Self-hosted archival for public ChatGPT share links. The service imports a
shared conversation once, normalizes it into a stable model, and publishes
immutable full-page and embeddable HTML snapshots backed by SQLite.

This project is not affiliated with, endorsed by, or sponsored by OpenAI.
ChatGPT and OpenAI are trademarks of their respective owner.

## Features

- Imports public `chatgpt.com/share/...` and legacy `chat.openai.com/share/...`
  links.
- Supports current React Router streaming payloads and legacy `__NEXT_DATA__`
  payloads.
- Stores metadata and revision state in SQLite using WAL mode.
- Writes raw payloads, normalized snapshots, and rendered HTML to persistent
  storage.
- Publishes stable URLs and content-addressed immutable revisions.
- Produces both full-page and iframe-friendly embed views.
- Issues one-time management tokens for refresh, preview, and delete actions.
- Optionally purges Cloudflare cache entries after lifecycle changes.
- Ships as a non-root, multi-architecture container image.

The detailed architecture and security model are documented in
[docs/design.md](docs/design.md).

```text
ChatGPT Share URL -> fetch -> extract -> normalize
    -> SQLite (metadata, revisions, token hashes)
    -> files (raw.json, snapshot.json, page.html, embed.html)
    -> /c/<slug>/r/<revision> and /e/<slug>/r/<revision>
```

## Quick Start

### Docker

Container images for `linux/amd64` and `linux/arm64` are published to GitHub
Container Registry after pushes to `main` and tags matching `v*`.

```sh
docker pull ghcr.io/jihuayu/chatgpt-share-page:latest
docker run --rm -p 8080:8080 \
  -v chatgpt-share-data:/data \
  -e PUBLIC_BASE_URL=http://localhost:8080 \
  -e APP_BASE_URL=http://localhost:8080 \
  ghcr.io/jihuayu/chatgpt-share-page:latest
```

If the package requires authentication, log in with a GitHub token that has
`read:packages` permission before pulling:

```sh
printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u <github-username> --password-stdin
```

Open `http://localhost:8080`, submit a public ChatGPT share URL, and store the
management token returned for a newly created snapshot. The plaintext token is
shown once and cannot be recovered later.

### Run From Source

Requirements:

- Go 1.25 or newer
- A writable directory for SQLite and generated artifacts

```sh
go run ./cmd/server
```

To build a local container instead:

```sh
docker build -t chatgpt-share-page .
docker run --rm -p 8080:8080 \
  -v chatgpt-share-data:/data \
  -e PUBLIC_BASE_URL=http://localhost:8080 \
  -e APP_BASE_URL=http://localhost:8080 \
  chatgpt-share-page
```

## Configuration

Configuration is read from environment variables.

| Variable | Default | Description |
| --- | --- | --- |
| `PORT` | `8080` | HTTP listen port |
| `DATA_DIR` | `./data` | Root directory for generated artifacts |
| `DATABASE_PATH` | `./data/app.db` | SQLite database path |
| `PUBLIC_BASE_URL` | `http://localhost:8080` | Public origin used in page and embed URLs |
| `APP_BASE_URL` | `http://localhost:8080` | Application and API origin reserved for split deployments |
| `MAX_IMPORT_CONCURRENCY` | `2` | Maximum concurrent imports |
| `FETCH_TIMEOUT_SECONDS` | `30` | Default upstream request timeout |
| `MAX_FETCH_BYTES` | `20971520` | Maximum upstream HTML response size |
| `MAX_REQUEST_BYTES` | `1048576` | Maximum API request body size |
| `MAX_REDIRECTS` | `3` | Maximum allowed redirects while fetching |
| `CLOUDFLARE_API_TOKEN` | empty | Optional Cloudflare API token for cache purge |
| `CLOUDFLARE_ZONE_ID` | empty | Optional Cloudflare zone ID for cache purge |
| `LOG_LEVEL` | `info` | Structured log level |

For an internet-facing deployment, set `PUBLIC_BASE_URL` and `APP_BASE_URL` to
the external HTTPS origins. Persist both `DATA_DIR` and `DATABASE_PATH`; the
container defaults place them under `/data`.

## API

### Import A Snapshot

```http
POST /api/v1/snapshots
Content-Type: application/json

{
  "url": "https://chatgpt.com/share/<share-id>",
  "title": "Optional title override",
  "include_hidden": false,
  "all_nodes": false,
  "timezone": "Asia/Shanghai",
  "timeout_seconds": 30
}
```

A new import returns `id`, `slug`, `revision`, `page_url`, `embed_url`, and a
one-time `admin_token`. Re-importing the same source returns the existing
snapshot with `existing: true` and does not issue another token.

### Manage A Snapshot

The following endpoints require `Authorization: Bearer <admin-token>`:

```text
GET    /api/v1/snapshots/{id}
POST   /api/v1/snapshots/{id}/refresh
DELETE /api/v1/snapshots/{id}
POST   /api/v1/previews
```

`POST /api/v1/previews` accepts `snapshot_id`, `kind` (`page` or `embed`), and
optional import overrides. Preview output is never persisted or cached.

### Public Routes

```text
GET /c/<slug>                  -> redirects to the active full-page revision
GET /c/<slug>/r/<revision>     -> immutable full-page snapshot
GET /e/<slug>                  -> redirects to the active embed revision
GET /e/<slug>/r/<revision>     -> immutable embed snapshot
GET /healthz                   -> {"status":"ok"}
```

Example embed:

```html
<iframe
  src="https://share.example.com/e/<slug>/r/<revision>"
  title="AI conversation"
  loading="lazy"
  sandbox="allow-scripts allow-popups"
  style="width:100%;border:0;min-height:480px"
></iframe>
```

## Security

- The fetcher accepts only HTTPS share URLs on the supported OpenAI domains.
- Redirects are revalidated and DNS results that resolve to non-public IP
  ranges are rejected to reduce SSRF risk.
- Request and upstream response sizes, redirect counts, import concurrency, and
  timeouts are bounded.
- Rendered Markdown is sanitized before publication, and public pages use a
  restrictive Content Security Policy.
- Management tokens use high-entropy randomness. Only SHA-256 token hashes are
  persisted, and authorization headers are not logged.
- Public snapshots default to `noindex` and do not require reader credentials.
- Local databases, environment files, private keys, and generated artifacts are
  excluded from version control.

Do not import conversations containing information you are not authorized to
archive or publish. Removing the original ChatGPT share link does not
automatically remove an imported snapshot.

## Development

```sh
go vet ./...
go test ./...
go test -race ./...
go build ./cmd/server
```

The container publishing workflow is defined in
[.github/workflows/publish-ghcr.yml](.github/workflows/publish-ghcr.yml).

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).
