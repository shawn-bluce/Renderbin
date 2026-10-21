# Renderbin

[English](README.md) | **中文**

一个自托管的文件分享服务：上传 HTML、Markdown 或文本文件，或者一个带 `index.html` 的整个文件夹，就能得到一个链接，打开即是渲染好的页面。它是为 AI 助手生成的那类自包含报告页准备的；借助内置的 **MCP 服务**，Agent 可以自己上传文件，再把链接交给你。

整个服务就是一个二进制文件（或一个容器），所有数据都在一个目录里：一个 SQLite 数据库，加上旁边存放的文件内容。

![Renderbin 管理界面截图](doc/screenshot.png)

## 功能

- **格式**：HTML（原样输出）、Markdown（渲染后输出）、纯文本，以及多页面站点（文件夹或 `.zip`）
- **分享**：文件可设为私有或公开；公开链接带随机访问码，可按时间或访问次数过期
- **管理**：标签、搜索、回收站、每账号存储配额、一键备份与恢复
- **账号**：多用户；第一个账号是超级管理员，是否开放注册可开关
- **面向 Agent**：MCP 服务，以及使用同一个用户 API Key 的 REST API
- **界面**：中英双语，浅色与深色主题

## 快速开始

```bash
curl -O https://raw.githubusercontent.com/shawn-bluce/renderbin/master/docker-compose.yml
docker compose up -d
```

打开 `http://127.0.0.1:8080`，在欢迎页创建第一个账号，它就是超级管理员。容器只监听 `127.0.0.1`，请在前面放一个 Nginx 或 Caddy，并转发 `X-Forwarded-Proto`（如果代理改写了主机名，还要转发 `X-Forwarded-Host`）。

升级：`docker compose pull && docker compose up -d`。所有数据都在 `db-data` 卷里（`docker compose down -v` 会把它删掉）。想先知道升级会做什么，可以在 `up -d` 之前运行 `docker compose pull && docker compose run --rm app migrate --check`：它不做任何改动，只报告新版本要不要搬数据、要搬多少（也就是会停服多久）、磁盘空间够不够。

备份可以在 设置 → 备份 里下载，也可以走命令行；数据量大时命令行比浏览器下载更可靠：

```bash
docker compose exec app ./server backup --out=/data/backup.tar   # 然后把它拷到别的机器上
docker compose exec app ./server restore --in=/data/backup.tar --yes
docker compose exec app ./server fsck                            # 检查每个文件的内容是否完好
```

只拷贝 `app.db` 不算备份：它只记录文件内容放在哪，并不包含内容本身。

不用 Docker 的话，可以从最新 release 下载静态 Linux 二进制（amd64 或 arm64）：

```bash
curl -LO https://github.com/shawn-bluce/renderbin/releases/latest/download/renderbin_linux_amd64.tar.gz
tar xzf renderbin_linux_amd64.tar.gz && ./renderbin
```

环境变量有三个，其余配置都在应用的设置页里。

| 变量          | 默认值                     | 说明              |
| ------------- | -------------------------- | ----------------- |
| `LISTEN_ADDR` | `:8080`                    | 监听地址          |
| `DB_PATH`     | `data/app.db`              | SQLite 数据库路径 |
| `BLOB_DIR`    | `DB_PATH` 同目录下的 `blobs` | 文件内容存放目录  |

## MCP 与 API

在 **设置 → AI 能力** 中开启 MCP，拿到你的 API Key，然后把服务添加到客户端：

```bash
claude mcp add --transport http renderbin https://your-host/mcp \
  --header "Authorization: Bearer rb_..."
```

这些工具只能操作 Key 所属账号自己的文件：上传、列出、搜索、修改、发布（可附带时间或次数限制）、取消发布、删除（移入回收站），以及用于多页面站点的 `upload_site` / `update_site`。新上传的文件默认是私有的。

同一个 Key 也可以作为 Bearer Token 调用 REST API，例如在命令行里上传一个站点：

```bash
cd my-site && zip -r ../site.zip . && cd ..
curl --data-binary @site.zip -H "Content-Type: application/zip" \
  -H "Authorization: Bearer rb_..." "https://your-host/api/sites?name=My%20Site"
```

关闭 MCP 或停用账号后，Key 随即失效；它也无法访问备份、账号管理等管理员功能。

## 多页面站点

上传一个顶层有 `index.html` 的文件夹，连同它用到的页面、CSS、JS 和数据文件。可以点 **上传文件夹**，把文件夹或 `.zip` 拖到面板上，或者用 **上传** 按钮选一个 `.zip`。文件之间的相对链接无需任何修改即可正常工作。

- 上限：解压后 5 MB、200 个文件，上传的 zip 不超过 8 MB。
- 访问次数限制只统计 HTML 页面的浏览，CSS、JS 和数据文件不计。
- 不支持 `/style.css` 这样以 `/` 开头的路径，请用相对路径；路径区分大小写。
- 修改站点需要整包替换文件夹，或用 MCP 的 `update_site` 工具修改单个文件。

## 安全

- 上传的 HTML 原样运行（包括脚本），但处在 CSP 沙箱中：它读不到访问者的 Cookie，也无法以访问者的身份调用 API。需要更强的隔离时，可以把 `/res` 放到单独的域名下。
- 每个账号只能看到自己的文件。超级管理员负责设置、账号和备份，但无法打开其他用户的文件。
- 单个文件最大 5 MB，每个账号有存储配额（默认 100 MB，超级管理员可调整）。
- 没有邮件找回密码。其他账号由超级管理员在设置页重置；超级管理员自己被锁在外面时，运行 `docker compose exec app ./server reset-password --user=NAME`。
- 报告漏洞请开 issue 说明影响范围，修复发布前请不要附上利用细节。

## 本地开发

后端用 Go，前端用 SvelteKit；前端构建产物会嵌入 Go 二进制。

```bash
make dev-api   # Go 服务，监听 :8080
make dev-web   # Vite 开发服务器，监听 :5173，请求代理到 Go 服务
```

其他命令：`make build`、`make test`、`make check`、`make sqlc`（见 [`Makefile`](Makefile)）。提 PR 前请确保 `make check` 和 `make test` 都能通过。架构说明见 [`CLAUDE.md`](CLAUDE.md)。

## 许可证

[MIT](LICENSE)
