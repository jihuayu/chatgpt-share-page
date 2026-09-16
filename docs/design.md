# ChatGPT Share Page 设计文档

- 状态：提案
- 创建日期：2026-09-11
- 技术栈：Go、SQLite、HTML/CSS/少量原生 JavaScript
- 一期部署假设：单个 Go 实例 + 持久化磁盘 + Cloudflare 反向代理与缓存
- 一期明确不考虑：中国大陆网络优化、ICP 备案、国内 CDN、多实例高可用、对象存储迁移

## 1. 背景与目标

本项目把公开的 ChatGPT Share 链接导入为独立的对话快照，再使用自己的域名重新渲染为完整展示页和嵌入页。

目标不是给 ChatGPT Share 页面套一层 iframe，也不是把服务做成通用 URL 代理，而是：

```text
ChatGPT Share URL
        |
        v
  Go 导入与解析
        |
        v
标准化 ConversationSnapshot
        |
        +--> SQLite 元数据与版本状态
        |
        +--> 持久化 raw/snapshot/HTML 文件
        |
        v
 Cloudflare 缓存公开页面
```

用户最终访问的是本项目域名下的页面，读者不需要再次请求 `chatgpt.com`。

目标：

1. 导入一个公开 ChatGPT Share 链接。
2. 兼容当前已验证的 React Router 流式载荷和旧版 `__NEXT_DATA__` 载荷。
3. 将对话转换为稳定的内部 JSON 模型。
4. 在发布时生成不可变 revision 的完整页和嵌入页。
5. 通过 Cloudflare 缓存降低公开访问的源站压力。
6. 提供可删除、可更新、可追踪的快照生命周期。

一期不做：

1. 自动同步原始 ChatGPT Share 链接。
2. 通过 ChatGPT Cookie 访问私有对话。
3. 任意 URL 抓取或通用代理。
4. 图片、上传文件、音频、视频的自动转存。
5. 公开搜索、推荐、评论、点赞和复杂账号体系。
6. 中国大陆访问优化和国内 CDN 接入。

## 2. 参考材料与结论

### 2.1 参考项目

参考项目：<https://github.com/jihuayu/chatgpt-share-to-md>

当前参考实现已经验证了以下边界：

- 使用 Go 标准库提供 `GET /healthz` 和 `POST /api/v1/export`。
- 只接受 `https://chatgpt.com/share/<id>` 或 `chat.openai.com` 对应路径。
- 请求体限制为 1 MiB，远端 HTML 响应体限制为 20 MiB。
- 每次重定向都会重新校验目标是否仍为允许的 Share 域名。
- 支持从 `streamController.enqueue(...)` 提取压缩引用表载荷。
- 兼容 `__NEXT_DATA__` JSON 载荷。
- 可以过滤视觉隐藏消息，并支持 `include_hidden`、`all_nodes`、`timezone`、`timeout_seconds` 等导出选项。
- 使用明确的错误类型区分 URL 错误、抓取错误、解析错误和内部错误。

新项目将复用其解析器思路，但不会把 Markdown 导出 API 直接当成最终产品模型。导入结果需要进一步保存为快照、revision 和 HTML 产物。

### 2.2 设计结论

以下结论来自项目早期设计讨论，已整理并固化在本文档中，不依赖外部共享会话长期可用。

设计文档给出的核心判断：

- 不使用 iframe 直接嵌入 ChatGPT Share 页面。
- 将 ChatGPT 对话视为一种可导入的内容源，而不是长期在线同步源。
- 内容应保存为结构化 JSON，HTML 只是某个 renderer 版本下的产物。
- 对话页适合在导入时静态生成，而不是每次请求都 SSR。
- 稳定 URL 和不可变 revision URL 应当分离。
- 完整展示页和嵌入页应在发布时分别生成。
- 公开页适合使用 `unlisted` 和 `noindex` 默认值。

本项目在一期采用更轻的 Go 单体实现：动态导入、管理、删除由 Go 处理；公开页优先读取已经生成的 HTML；Cloudflare 仅作为缓存和反向代理，不作为持久化存储。

## 3. 总体架构

### 3.1 组件

```text
                           +----------------------+
                           |      浏览器/博客      |
                           +----------+-----------+
                                      |
                            HTTPS / Cloudflare
                                      |
                 +--------------------+--------------------+
                 |                                         |
       app.example.com                             share.example.com
       导入/管理/API/预览                              展示页/嵌入页
                 |                                         |
                 +--------------------+--------------------+
                                      |
                              Go HTTP Server
                                      |
       +----------------------+-------+----------------------+
       |                      |                              |
  Import Service        SQLite Store                   File Store
       |                      |                              |
  Fetcher/Extractor     metadata/revision       raw/snapshot/html/assets
       |                      |                              |
       +----------------------+------------------------------+
```

一期默认使用同一个 Go 进程和同一个域名也可以运行。拆分为 `app` 与 `share` 两个域名是推荐部署形态，用于避免登录、预览和管理接口被公开缓存。

### 3.2 读写路径

导入路径：

```text
POST /api/v1/snapshots
    -> 校验请求
    -> 校验 Share URL
    -> 抓取 HTML
    -> 提取原始 conversation
    -> 标准化 snapshot
    -> 保存 raw.json
    -> 创建 revision
    -> 渲染 page.html 和 embed.html
    -> 原子写入文件
    -> SQLite 激活 revision
    -> 清理稳定地址缓存
```

公开读取路径：

```text
GET /c/:slug
GET /c/:slug/r/:revision
GET /e/:slug
GET /e/:slug/r/:revision
    -> 公开页优先读取生成好的 HTML
    -> revision URL 直接返回不可变文件
    -> 稳定 URL 解析当前 active revision
    -> 仅在产物丢失时从 snapshot.json 重新生成
    -> 设置公开缓存响应头
```

## 4. 技术选型

### 4.1 Go HTTP

一期直接使用 `net/http`，不引入重型 Web 框架：

- 路由数量有限。
- 请求和响应模型明确。
- 方便设置缓存、安全和超时策略。
- 与参考项目的 `httpapi` 包保持一致。
- 未来如果需要更复杂的路由，可以增加 `chi`，但不作为一期依赖。

### 4.2 SQLite

使用 `database/sql` 封装 SQLite，推荐 `modernc.org/sqlite`：

- 纯 Go 实现，不依赖 CGO，适合 Windows、本地开发和单二进制部署。
- 数据库只有元数据、revision 状态和管理密钥哈希，不保存大段 HTML 正文。
- 通过 WAL 提升读写并发。
- 单实例写入串行化，避免一期引入外部数据库。

SQLite 不承担公开页正文的高频读取。公开页内容由文件系统和 Cloudflare 缓存承载。

### 4.3 HTML 模板与静态生成

使用 `html/template`：

- 适合固定结构的完整页、嵌入页和管理页面。
- 自动 HTML 转义，避免把用户文本直接拼入 HTML。
- 模板可以通过 `//go:embed` 编译进二进制。
- 生成产物是普通 HTML，不需要浏览器加载 React runtime。

Markdown 渲染建议使用：

- `github.com/yuin/goldmark`：Markdown/GFM 渲染。
- `github.com/alecthomas/chroma/v2`：发布时完成代码高亮。
- `github.com/microcosm-cc/bluemonday`：对最终用户内容做 HTML 清理。

一期可以先复用参考项目已经验证的文本与代码块处理逻辑，再逐步替换为统一的 `ContentBlock` 渲染器。

### 4.4 文件存储

一期使用本地持久化目录：

```text
/data
  app.db
  conversations
    <snapshot-id>
      raw.json
      snapshot.json
      revisions
        <revision>
          page.html
          embed.html
```

Cloudflare 缓存不是持久化存储。文件必须始终保留在源站，才能应对缓存淘汰、源站重启和缓存清理。

## 5. 核心数据模型

### 5.1 ConversationSnapshot

内部格式不直接暴露 ChatGPT 原始 JSON，后续可以支持其他来源。

```go
type ConversationSnapshot struct {
    Version       int                `json:"version"`
    ID            string             `json:"id"`
    Title         string             `json:"title"`
    Source        SnapshotSource     `json:"source"`
    ImportedAt    time.Time          `json:"imported_at"`
    UpdatedAt     time.Time          `json:"updated_at"`
    Messages      []ConversationMsg `json:"messages"`
    Metadata      SnapshotMetadata   `json:"metadata"`
}

type SnapshotSource struct {
    Provider string `json:"provider"` // chatgpt
    URL      string `json:"url"`
    ShareID  string `json:"share_id"`
}

type SnapshotMetadata struct {
    ConversationID string `json:"conversation_id,omitempty"`
    MessageCount   int    `json:"message_count"`
    Model          string `json:"model,omitempty"`
    IncludeHidden  bool   `json:"include_hidden"`
    AllNodes       bool   `json:"all_nodes"`
    Timezone       string `json:"timezone"`
}

type ConversationMsg struct {
    ID        string        `json:"id"`
    Role      string        `json:"role"`
    CreatedAt *time.Time     `json:"created_at,omitempty"`
    Blocks    []ContentBlock `json:"blocks"`
    Hidden    bool          `json:"hidden"`
}

type ContentBlock struct {
    Type      string `json:"type"` // markdown/code/quote/citation/attachment
    Content   string `json:"content,omitempty"`
    Language  string `json:"language,omitempty"`
    Title     string `json:"title,omitempty"`
    URL       string `json:"url,omitempty"`
    AssetName string `json:"asset_name,omitempty"`
}
```

一期只发布文本、Markdown、代码、引用和附件元数据。图片和上传文件只保留文件名、MIME、大小等描述，不主动从 ChatGPT 下载并重新托管。

### 5.2 SQLite 表

#### `snapshots`

```sql
CREATE TABLE snapshots (
    id              TEXT PRIMARY KEY,
    slug            TEXT NOT NULL UNIQUE,
    title           TEXT NOT NULL,
    source_provider TEXT NOT NULL,
    source_url      TEXT NOT NULL,
    source_share_id TEXT NOT NULL,
    visibility      TEXT NOT NULL DEFAULT 'unlisted',
    noindex         INTEGER NOT NULL DEFAULT 1,
    status          TEXT NOT NULL DEFAULT 'active',
    active_revision TEXT,
    content_hash    TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL,
    deleted_at      TEXT
);
```

#### `snapshot_revisions`

```sql
CREATE TABLE snapshot_revisions (
    snapshot_id       TEXT NOT NULL,
    revision          TEXT NOT NULL,
    content_hash      TEXT NOT NULL,
    extractor_version TEXT NOT NULL,
    renderer_version  TEXT NOT NULL,
    snapshot_path     TEXT NOT NULL,
    page_path         TEXT NOT NULL,
    embed_path        TEXT NOT NULL,
    created_at        TEXT NOT NULL,
    PRIMARY KEY (snapshot_id, revision),
    FOREIGN KEY (snapshot_id) REFERENCES snapshots(id)
);
```

#### `snapshot_admin_tokens`

一期无账号体系时，使用一次性管理密钥管理快照。数据库只保存哈希：

```sql
CREATE TABLE snapshot_admin_tokens (
    snapshot_id TEXT PRIMARY KEY,
    token_hash  TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    revoked_at  TEXT,
    FOREIGN KEY (snapshot_id) REFERENCES snapshots(id)
);
```

实现要求：

- 管理密钥使用高熵随机值生成。
- 只在创建响应中返回一次明文密钥。
- 数据库存储 `SHA-256` 或带随机盐的密码哈希。
- 管理 API 使用 `Authorization: Bearer <token>`，不要把 token 写入 URL 日志。

### 5.3 revision 规则

- 每次内容发生变化都创建新的 revision。
- revision URL 永久指向同一份 HTML，不覆盖旧文件。
- `active_revision` 只在全部文件写入成功后更新。
- 稳定 URL 始终解析到当前 `active_revision`。
- 删除快照时先撤销稳定 URL，再按策略删除文件和数据库记录。

## 6. URL 与 API 设计

### 6.1 公开页面

```text
GET /c/:slug
GET /c/:slug/r/:revision
GET /e/:slug
GET /e/:slug/r/:revision
```

含义：

- `/c/:slug`：完整展示页，重定向或直接返回当前 revision。
- `/c/:slug/r/:revision`：不可变完整展示页。
- `/e/:slug`：当前 revision 的嵌入页。
- `/e/:slug/r/:revision`：不可变嵌入页。

推荐稳定地址返回 `302` 到当前 revision 地址，方便 Cloudflare 缓存不可变内容；如果后续需要隐藏 revision，也可以保留服务端直接发送文件的实现。

### 6.2 导入 API

```http
POST /api/v1/snapshots
Content-Type: application/json

{
  "url": "https://chatgpt.com/share/SHARE_ID",
  "title": "可选覆盖标题",
  "include_hidden": false,
  "all_nodes": false,
  "timezone": "Asia/Shanghai",
  "timeout_seconds": 30
}
```

成功响应：

```json
{
  "id": "01J...",
  "slug": "example-conversation",
  "revision": "sha256-prefix-or-ulid",
  "title": "Example conversation",
  "message_count": 12,
  "page_url": "https://share.example.com/c/example-conversation",
  "embed_url": "https://share.example.com/e/example-conversation",
  "admin_token": "returned-only-on-create"
}
```

处理语义：

- 相同 `content_hash` 已存在时可以复用现有 revision，不重复生成文件。
- 新导入内容默认 `visibility=unlisted`、`noindex=1`。
- 响应返回 `admin_token`，后续更新和删除必须携带它。

### 6.3 预览 API

```http
POST /api/v1/previews
Authorization: Bearer <admin-token>
Content-Type: application/json
```

预览只返回临时 HTML 或结构化 snapshot，不更新 `active_revision`，也不写入公开路径。预览结果应设置 `Cache-Control: no-store`。

### 6.4 更新 API

```http
POST /api/v1/snapshots/:id/refresh
Authorization: Bearer <admin-token>
Content-Type: application/json

{
  "url": "https://chatgpt.com/share/SHARE_ID",
  "include_hidden": false,
  "all_nodes": false,
  "timezone": "UTC"
}
```

更新流程仍然是创建新 revision，不修改旧 revision。

### 6.5 删除 API

```http
DELETE /api/v1/snapshots/:id
Authorization: Bearer <admin-token>
```

删除要求：

- 先将快照状态改为 `deleted`，使稳定地址立即不可用。
- 清理稳定 URL 缓存。
- 不必立即删除 revision 文件，可通过后台清理任务延迟删除。
- 不影响其他快照。

### 6.6 健康检查

```http
GET /healthz
```

返回：

```json
{"status":"ok"}
```

`/healthz` 不访问 ChatGPT，不依赖公开页面，不应被 Cloudflare 公开缓存。

## 7. 导入、解析与标准化

### 7.1 URL 校验

导入接口必须复用参考项目的严格校验：

- 只接受 HTTPS。
- 只接受 `chatgpt.com`、`www.chatgpt.com`、`chat.openai.com`、`www.chat.openai.com`。
- 路径必须符合 `/share/<share-id>`。
- 不接受用户自定义端口。
- 不携带用户 Cookie、Authorization 或浏览器本地凭证。

### 7.2 抓取限制

建议默认值：

| 项目 | 一期值 |
| --- | ---: |
| 单请求超时 | 30 秒 |
| 最大超时 | 300 秒 |
| 最大请求体 | 1 MiB |
| 最大远端 HTML | 20 MiB |
| 最大重定向 | 3 次 |
| 单实例并发导入 | 可配置，默认 2 |
| User-Agent | `chatgpt-share-page/<version>` |

每次重定向都必须重新执行允许域名校验，不能仅校验第一次 URL。

### 7.3 Payload 提取

提取器按以下顺序尝试：

1. 查找 `streamController.enqueue(...)` 中的 JSON 字符串。
2. 解码引用表和递归对象。
3. 查找具有 `mapping`、`linear_conversation`、`current_node` 或 `conversation_id` 特征的 conversation 对象。
4. 如果流式载荷不存在，再查找 `<script id="__NEXT_DATA__">`。
5. 如果页面包含浏览器挑战或无法找到 conversation，返回 `parse_failed`。

提取器不把整个 HTML 当作可信内容。原始页面只用于生成 `raw.json` 和提取结果，进入模板前必须经过标准化和转义。

### 7.4 标准化

标准化阶段负责：

- 过滤视觉隐藏消息，除非 `include_hidden=true`。
- 根据 `all_nodes` 决定使用完整节点树还是当前可见线性路径。
- 统一 `system`、`user`、`assistant`、`tool` 角色。
- 将文本 parts 转为 `markdown` block。
- 将代码内容和语言转为 `code` block。
- 将引用和链接转为安全的 `citation` block。
- 将图片、附件转换为元数据，不默认抓取外部资源。
- 规范时间和时区。
- 计算 canonical JSON 的 `content_hash`。

## 8. 渲染与页面结构

### 8.1 完整展示页

完整页包含：

1. 标题。
2. 来源标记和导入时间。
3. AI-generated conversation 标识。
4. 消息列表。
5. 代码块复制按钮。
6. 消息锚点。
7. 明暗主题 CSS。
8. 独立快照说明。
9. 可选的原始链接，不自动加载原始页面。

### 8.2 嵌入页

嵌入页只保留：

- 对话标题和消息内容。
- 主题 CSS。
- 代码复制功能。
- 少量 `postMessage` 高度同步脚本。

示例：

```html
<iframe
  src="https://share.example.com/e/example-conversation/r/01J..."
  title="AI conversation"
  loading="lazy"
  sandbox="allow-scripts allow-popups"
  style="width:100%;border:0;min-height:480px"
></iframe>
```

嵌入页不设置登录 Cookie，不读取父页面的敏感信息，不允许任意父页面通过消息修改内容。

### 8.3 安全渲染

- Go 模板使用 `html/template` 自动转义。
- Markdown 生成 HTML 后经过 sanitizer。
- 禁止用户内容中的 `<script>`、事件属性、`javascript:` URL、任意 iframe 和表单提交。
- 外链只允许 `https`，必要时只保留纯文本 URL。
- CSP 至少限制脚本来源、图片来源和连接来源。
- 公开页不设置 Session Cookie。

## 9. 发布一致性与故障恢复

发布不能直接覆盖正在被读取的文件。建议流程：

1. 生成临时目录 `revisions/.tmp-<id>`。
2. 写入 `snapshot.json`、`page.html`、`embed.html`。
3. 对每个文件执行 fsync 或至少确认写入成功并关闭文件。
4. 对 HTML 和 JSON 做最小完整性检查。
5. 原子重命名到最终 revision 目录。
6. 开启 SQLite 事务，插入 `snapshot_revisions` 并更新 `snapshots.active_revision`。
7. 提交事务。
8. 清理稳定 URL 的 Cloudflare 缓存。

如果任一步失败：

- 不更新 `active_revision`。
- 保留旧 revision 继续服务。
- 临时目录放入清理队列。
- 返回可识别的错误码。

如果 HTML 文件意外丢失，但 `snapshot.json` 仍存在，公开路由可以执行一次 SSR fallback：从 snapshot 重新渲染、写回文件后返回。fallback 不是常规访问路径，必须记录日志并限制并发。

## 10. Cloudflare 缓存策略

一期假设使用 Cloudflare 全球网络，不处理中国大陆专属接入。

### 10.1 缓存规则

公开 revision 页面：

```http
Cache-Control: public, max-age=86400
Cloudflare-CDN-Cache-Control: public, max-age=31536000, stale-if-error=604800
ETag: "<content-hash>"
```

稳定地址：

```http
Cache-Control: public, max-age=0, must-revalidate
Cloudflare-CDN-Cache-Control: public, max-age=60, stale-while-revalidate=86400
```

以下路径绕过缓存：

```text
/api/*
/admin/*
/preview/*
/healthz
```

公开展示域名只缓存 `GET` 和 `HEAD`。所有动态 API 使用 `no-store` 或不设置可公开缓存的响应头。

### 10.2 缓存清理

更新或删除快照时清理：

- `/c/:slug`
- `/e/:slug`

不可变 revision URL 不需要因为新版本生成而清理。删除策略可以选择立即清理 revision URL，或由后台延迟清理。

### 10.3 Cloudflare 的边界

- Cloudflare 只做加速和缓存，不替代源站文件和 SQLite。
- 缓存淘汰后，页面必须能够从源站恢复。
- 不允许公开页设置会导致缓存绕过的 Session Cookie。
- 访问统计通过独立请求完成，不在 HTML 首次请求中查询 SQLite。

## 11. 包结构

建议目录：

```text
cmd/server/main.go
internal/
  config/
    config.go
  httpapi/
    handler.go
    middleware.go
  fetcher/
    chatgpt.go
    client.go
  extractor/
    chatgpt.go
    payload.go
  conversation/
    model.go
    normalize.go
    hash.go
  renderer/
    renderer.go
    markdown.go
    templates/
      page.html
      embed.html
      message.html
  publish/
    service.go
    revision.go
  storage/
    database.go
    migrations.go
    files.go
  cache/
    cloudflare.go
  security/
    token.go
    url.go
web/
  static/
    app.css
    embed.js
data/
  .gitkeep
```

参考项目的 `exporter` 和 `httpapi` 可以先迁移为独立内部包，再逐步加入 `conversation`、`publish` 和 `storage`，避免把抓取、解析、渲染、数据库事务全部堆在一个 handler 中。

## 12. 错误码

统一响应格式：

```json
{
  "error": {
    "code": "invalid_url",
    "message": "only HTTPS ChatGPT share URLs are supported",
    "request_id": "..."
  }
}
```

一期错误码：

| HTTP | code | 含义 |
| ---: | --- | --- |
| 400 | `invalid_json` | JSON 结构错误或未知字段 |
| 400 | `missing_url` | 缺少 Share URL |
| 400 | `invalid_url` | 非允许的 Share URL |
| 400 | `invalid_option` | timeout、timezone 等参数错误 |
| 401 | `missing_token` | 缺少管理密钥 |
| 403 | `invalid_token` | 管理密钥无效 |
| 404 | `snapshot_not_found` | 快照或 revision 不存在 |
| 404 | `snapshot_deleted` | 快照已删除 |
| 413 | `request_too_large` | 请求体或远端响应超限 |
| 502 | `fetch_failed` | ChatGPT 页面下载失败 |
| 422 | `parse_failed` | 页面没有可识别的 conversation |
| 409 | `revision_conflict` | 并发更新冲突 |
| 500 | `storage_failed` | SQLite 或文件写入失败 |
| 500 | `render_failed` | HTML 生成失败 |

## 13. 并发与运行时配置

环境变量建议：

```text
PORT=8080
DATA_DIR=./data
DATABASE_PATH=./data/app.db
PUBLIC_BASE_URL=https://share.example.com
APP_BASE_URL=https://app.example.com
MAX_IMPORT_CONCURRENCY=2
FETCH_TIMEOUT_SECONDS=30
MAX_FETCH_BYTES=20971520
CLOUDFLARE_API_TOKEN=
CLOUDFLARE_ZONE_ID=
LOG_LEVEL=info
```

要求：

- `CLOUDFLARE_API_TOKEN` 不写入日志和数据库。
- 没有 Cloudflare 配置时，更新仍然成功，但记录缓存清理失败并允许手工清理。
- SQLite 使用 WAL，应用启动时执行迁移。
- 导入任务使用有界并发，防止单个实例同时抓取大量远端页面。
- 公开读取不等待导入任务，不在读路径执行远端抓取。
- 服务退出前等待正在进行的发布事务完成，并设置最长 shutdown timeout。

## 14. 测试计划

### 14.1 单元测试

- Share URL 校验：HTTPS、允许域名、路径、查询参数、非法端口。
- 重定向重新校验 host 和 path。
- React Router 流式载荷解码。
- `__NEXT_DATA__` fallback 解码。
- 引用表循环引用和异常索引。
- hidden message 过滤。
- `all_nodes` 与线性 conversation 选择。
- Markdown、代码块、链接和附件元数据标准化。
- slug 和 content hash 稳定性。
- 管理 token 哈希校验。

### 14.2 HTTP 测试

- `/healthz` 方法和响应格式。
- `POST /api/v1/snapshots` 成功和错误映射。
- 未知 JSON 字段拒绝。
- 请求体超限。
- 缺少、错误和过期管理 token。
- 公开页的 `Cache-Control`、`ETag`、`X-Robots-Tag`。
- 删除后的稳定地址和 revision 地址行为。

### 14.3 发布一致性测试

- 渲染失败时旧 revision 仍然 active。
- 写入 HTML 失败时不更新 SQLite active revision。
- SQLite 提交失败时不暴露新 revision。
- 进程重启后可以从数据库和文件恢复。
- HTML 丢失时 snapshot fallback 只执行一次并能恢复文件。

### 14.4 安全测试

- Markdown XSS。
- `javascript:`、事件属性、任意 iframe、表单注入。
- SSRF 重定向、解析到内网 IP、IPv6 link-local、DNS rebinding 风险。
- 超大响应和慢响应。
- token 不出现在请求日志、错误日志和公开 URL 中。

### 14.5 集成验收

使用一个公开且稳定的 Share URL 验证：

1. 导入成功。
2. SQLite 出现 snapshot 和 revision。
3. `raw.json`、`snapshot.json`、`page.html`、`embed.html` 均存在。
4. 完整页和嵌入页内容一致但布局不同。
5. 公开页在无 ChatGPT 网络请求的情况下可以加载。
6. 更新生成新 revision，旧 revision 仍可访问。
7. 删除后稳定地址不可用。

## 15. 观测与运维

日志字段至少包含：

```text
request_id
operation
snapshot_id
revision
source_host
duration_ms
status
error_code
```

不记录：

- 管理 token。
- 用户 Cookie。
- 完整原始 ChatGPT HTML。
- 未清理的对话正文。

建议增加以下指标：

- `imports_total{status}`
- `imports_duration_seconds`
- `extract_failures_total{reason}`
- `render_failures_total`
- `publish_failures_total{stage}`
- `public_cache_purge_failures_total`
- `public_render_fallback_total`

一期可以先使用结构化日志和 `/healthz`，不引入完整 Prometheus/Grafana 体系。

## 16. 实施阶段

### Phase 1：基础导入与存储

- 从参考项目迁移 extractor 和 HTTP 错误模型。
- 加入 SQLite migration、snapshot、revision 表。
- 实现本地文件存储和原子发布。
- 实现 `POST /api/v1/snapshots` 和 `GET /healthz`。

### Phase 2：渲染页面

- 引入 `ConversationSnapshot`。
- 实现完整页和嵌入页模板。
- 加入 Markdown、代码高亮和 sanitizer。
- 实现 `/c/:slug`、`/c/:slug/r/:revision`、`/e/:slug`、`/e/:slug/r/:revision`。

### Phase 3：生命周期管理

- 管理 token。
- refresh、新 revision、删除和状态检查。
- 稳定地址缓存清理。
- HTML 丢失时 fallback 重建。

### Phase 4：部署与验证

- Docker 镜像和非 root 运行。
- 持久化挂载 `/data`。
- Cloudflare Cache Rules 和按 URL purge。
- 使用真实 Share URL 做端到端验收。
- 备份 SQLite 和 `data/conversations`。

## 17. 关键决策与未决问题

### 已决策

- 使用 Go 作为后端和发布时静态生成器。
- 使用 SQLite 保存元数据和 revision 状态。
- 使用本地持久化磁盘保存 raw、snapshot 和 HTML。
- 公开页面导入时生成，不使用传统全站构建。
- revision 页面不可变，稳定地址指向 active revision。
- Cloudflare 只做缓存，不作为数据源。
- 一期不考虑中国大陆访问问题。

### 未决问题

1. 是否允许未登录用户创建的快照永久保留，还是增加过期时间。
2. 删除时是否立即清理所有旧 revision 文件。
3. 是否将管理 token 升级为用户账号体系。
4. 图片和附件未来采用用户主动上传，还是仅保存原始元数据。
5. 是否需要兼容更多 AI 对话来源。
6. 单实例数据量达到什么规模后迁移到对象存储和 PostgreSQL。

## 18. 验收标准

一期完成的判定标准：

- 能使用一个有效 ChatGPT Share URL 创建快照。
- 解析失败时返回稳定、可识别的错误码。
- 快照能够生成完整展示页和嵌入页。
- 公开页不依赖访问者直接访问 ChatGPT。
- 更新不会破坏旧 revision。
- 删除能够阻断稳定地址访问。
- SQLite 和文件系统在重启后能够恢复。
- Cloudflare 缓存规则不会缓存导入、管理和预览接口。
- XSS、SSRF、超大响应和管理 token 泄漏测试通过。
