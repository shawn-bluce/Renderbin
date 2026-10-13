# Changelog

Notable changes per release. Versions follow [semantic versioning](https://semver.org/),
with the caveat that this is 0.x software: minor versions may break things, and
this file is where those breaks are written down.

Upgrades are `docker compose pull && docker compose up -d`. Database migrations
are forward-only and apply themselves on start, so **take a backup before
upgrading** (Settings → Backup) if you might want to go back.

## v0.4.0

### Changed

- Internal clean-up with no API change: the `reset-password` CLI now applies the
  same password rules as the web app (a password over bcrypt's 72-byte limit
  gets a clear message instead of a bcrypt error); a rejected backup upload no
  longer leaks a database handle; migration `0005` drops an index no query
  used; `server.New` takes just the connection; the `google/uuid` direct
  dependency is gone.

- **New look.** The whole UI is redesigned on Gruvbox, with a light and a dark
  theme (follows the OS until you pick one; toggle in the sidebar/top bar). On
  wide screens the dashboard gets a sidebar (All / Public / Private / Trash,
  storage meter) and uses the full width; on phones it becomes a top bar with
  chips and stacked file rows, and dialogs dock to the bottom as sheets.
  Faster to load: no webfonts, and only the icons actually used are bundled
  (JS+CSS went from ~213KB to ~119KB gzipped).
- A failed request now shows a translated message instead of a raw English
  "Failed to fetch"; the server's own text is shown only when it answered.
- The dashboard and settings load their data in parallel, search has a clear
  (×) button, and several controls got proper accessible names.

### Added

- **Multi-page sites.** Upload a directory — `index.html` plus the pages,
  stylesheets, scripts and data files it uses — as one file of the new kind
  `site`. Relative references and links between its files work unchanged. Upload
  a folder from the dashboard (button or drag-and-drop), a `.zip`, or
  `POST /api/sites` with a zip body; replace a site's content with
  `PUT /api/files/{slug}/site`. MCP gains `upload_site` and `update_site` (a
  patch: only the files that changed). Limits: 5 MB uncompressed, 200 files.
- Sites are served at `/res/{slug}/{token}/{path}`, where the token is the
  access code or, for the owner of a private site, a signed preview token valid
  for 6 hours. The existing share link `/res/{slug}?code=...` redirects there, so
  links already handed out keep their format.

### Behaviour changes to know about before upgrading

- **A slug or access code can no longer be `.` or `..`.** A browser removes such
  a segment from a site URL before sending the request. A single document with
  such a slug was never reachable either.
- **A site's `max_views` counts HTML page views**, not link openings: every page
  opened inside the site uses one view, and its stylesheets and scripts use none.
- **Rolling back** to an earlier version after creating a site needs a restored
  backup, as with any migration; an older binary cannot render sites at all.

## v0.3.0

### Added

- **An API key can call the REST API directly**, not just MCP: send it as
  `Authorization: Bearer rb_...`. Same rules as MCP — it lives and dies with the
  MCP toggle and the account's status — and it stays a file-scope credential, so
  backup and account management still require a browser session.
- **A default visibility for new uploads.** `upload_default_public` in Settings
  decides whether a newly created file starts public; unset means private.

## v0.2.0

### Breaking

- **`GET /api/files/search` moved to `GET /api/search`.** A static segment at the
  `/api/files/{slug}` level shadows any file whose custom slug happens to match
  it, so that file's own endpoint silently answered with the search handler's
  response. Scripts calling the old path now get a 404. Emptying the trash moved
  to `DELETE /api/trash` for the same reason.
- **Request bodies are capped.** Endpoints that take a small JSON payload now
  reject anything over 64 KB with `413`. Documents keep their own, much larger
  limit (5 MB of content); backup restore allows 256 MB and MCP 32 MB.
- **Release tarballs no longer carry the version in their filename**:
  `renderbin_linux_amd64.tar.gz`, not `renderbin_v0.2.0_linux_amd64.tar.gz`. The
  old name could never be fetched through `/releases/latest/download/`, which is
  the URL the README documents — so that quickstart was broken for v0.1.0. Pin
  the version in the URL path if you need a specific release. The version is
  stamped into the binary and reported by `GET /api/health`.

### Behaviour changes to know about before upgrading

- **Every account gets a 100 MB storage quota.** Existing accounts included. An
  account already storing more than that keeps every file it has, but cannot
  upload or edit until the super admin raises its limit in
  **Settings → Accounts**. Trashed files count toward the quota, because they
  still occupy the database.
- **Uploaded pages are sandboxed.** `/res/{slug}` now sends
  `Content-Security-Policy: sandbox allow-scripts …`. Scripts, links, forms and
  downloads still work, but a shared document can no longer call this app's API
  as whoever is viewing it. If you were relying on a published page talking to
  the API from the same origin, it will stop working.
- **The super admin can no longer reset their own password** through
  Settings → Accounts; use the profile section (which asks for the current
  password) or the `reset-password` CLI subcommand.

### Added

- **Account management**: create an account (the password is generated and shown
  once), delete an account together with all of its files, and set a per-account
  storage quota — alongside the existing suspend/restore and password reset.
- **Storage visibility**: every file row shows its size, the dashboard shows
  usage against the quota, and the accounts list shows both per account.
  `GET /api/user/usage` exposes the same figures.
- `reset-password` CLI subcommand, for the one lockout the app cannot fix from
  inside: `docker compose exec app ./server reset-password --user=NAME`.

### Fixed

- **First-run setup was a race.** Concurrent requests to `/api/setup` each
  created an account; only the first became the super admin. A double-clicked
  welcome form left a stray account, and a freshly exposed instance could be
  raced for ownership.
- **A password over 72 bytes returned "internal error".** bcrypt's own limit is
  in bytes, so an ordinary 25-character Chinese passphrase was rejected with no
  explanation on the very first screen. Nicknames are likewise now measured in
  characters rather than bytes.
- **Unmatched `/api/…` paths returned `200` and the SPA's HTML** instead of a
  JSON 404, so a typo in a client surfaced as an opaque JSON parse error and
  monitoring saw a healthy 200 for a route that does not exist.
- **An oversized upload reported "invalid request body"** instead of naming the
  5 MB limit, and a legal 5 MB document could trip it because of JSON escaping.
- **Copy-to-clipboard silently did nothing over plain HTTP.** It now falls back,
  and shows the link in a selectable field if the browser refuses outright.
- **The trash list went stale**, so a file deleted after visiting the Trash tab
  appeared in neither list and looked like it had been destroyed.
- **Restoring a backup overwrote the migration ledger**, which could make the
  running binary skip a migration permanently.
- **Listing files read every document's full source out of the database** only
  to discard it. An account holding 160 MB drove the server to ~350 MB of memory
  to answer a 13 KB request; it is now ~68 MB.
- Refreshing a file's access code now asks for confirmation — it invalidates
  every link already shared, with no undo.
- Uploads, tags and names are bounded, so a long name can no longer overflow a
  reverse proxy's header buffer and turn downloads into a 502.

### Security

- Uploaded HTML and Markdown are sandboxed (see above). Combined with the
  self-reset fix, a document uploaded by one account and opened by another can
  no longer act as that viewer.
- `X-Content-Type-Options`, `Referrer-Policy: no-referrer` and `X-Frame-Options`
  on every response. The referrer policy matters because a file's access code
  travels in the query string.
- Updated `modelcontextprotocol/go-sdk` to v1.4.1 and the Go toolchain to
  1.26.5, clearing 13 vulnerabilities that `govulncheck` reported as reachable.

## v0.1.0

First release.
