<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.png">
    <img src="assets/logo-light.png" alt="Dolmen logo" width="128">
  </picture>
</p>

# Dolmen — a private gist service

A stateless private gist service in Go, similar to GitHub Gists. Everything
lives in S3. The binary embeds the templates, the stylesheet, the fonts, and
the vendored JavaScript, so it needs no files on disk and makes no external
requests at runtime — Mermaid and MathJax ship in the binary, not from a
CDN. `go build` is the whole build.

## Features

- **Authentication**: OIDC login, tested with Dex. The app derives an
  opaque per-user ID by hashing the ID token's `sub` claim; that is the
  storage namespace. The `email` claim is carried for display only. The
  session cookie is the ID token and expires one hour after login. The app
  has no logout page; clear the cookie or wait for the expiry.
- **Storage**: S3 only, no database. Any path-style S3 provider works: AWS,
  Exoscale SOS, s3mock.
- **Versioning**: Gist history is S3 object versioning. The app enables
  versioning on your bucket at startup. You cannot turn versioning off
  afterwards, so the `s3.bucket` setting must name a bucket the app owns
  alone.
- **Rendering**: Markdown supports GFM tables, strikethrough, task lists,
  autolinks, footnotes, `:emoji:` shortcodes, GitHub alerts
  (`> [!NOTE]`-style blockquotes), heading permalink anchors, `![alt](url
  =WxH)` image sizing, and YAML front matter rendered as a table. Raw HTML
  is sanitized to a safe subset like GitHub: `<details>` collapsibles,
  `<kbd>`, and friends survive; scripts, frames, and event handlers don't.
  Code files get syntax highlighting with line numbers, driven by the file
  extension. Fenced code blocks in Markdown get highlighting too.
- **Naming**: The first file's name titles the gist. A submitted file without
  a name becomes `gistfile.txt`, `gistfile.1.txt`, and so on. Two files with
  the same name are a save error; the app never renames a file silently. The
  description is optional and shows under the name.
- **Preview**: A Markdown file on the create or edit page gets Edit and
  Preview tabs. Preview sends your unsaved draft, including the typed file
  name, to the server and shows exactly what saving would render. This needs
  JavaScript; without it the form works and the tabs stay hidden. The row
  buttons are click-only, so Tab moves from the file name to the content.
- **Diagrams and math**: A ` ```mermaid ` fence becomes a rendered diagram.
  Math uses the delimiters GitHub documents: `$...$` for inline and `$$...$$`
  for display. The binary ships Mermaid (12.1.0) and MathJax (3.2.2)
  bundled, and loads them only on pages that need them.
- **Raw access**: `GET /raw/{id}` serves the first file and
  `GET /raw/{id}/{filename}` serves a named one as `text/plain`. An attached
  file answers with a redirect straight to S3 (see Inline media). A Copy
  button sits on each file row and on each fenced code block in rendered
  Markdown. Scripts can send the ID token in a header instead of a cookie:

  ```
  curl -H "Authorization: Bearer <id-token>" http://localhost:8080/raw/{id}
  ```

  A `Bearer` token is a full session, not a scoped API key: it is accepted
  on every route the cookie session reaches. Treat leaked tokens as leaked
  logins.

- **Attachments**: Drop files onto the create or edit form, or paste them.
  Text-ish files (no NUL byte in the first 8 KiB, up to 8 MiB) fill the
  textarea as editable content. Everything else uploads from the browser
  straight to S3 over a presigned URL, so the bytes never reach the app
  server. The app infers the file name; a paste that carries no name becomes
  `pasted-1.png` and counting. The row then shows an "attached" badge. A
  paste of several files fills the empty rows first, then adds new rows,
  like a drop on the page; a single-file paste targets the row you are
  typing in, like a drop on that row. A drop or paste that would replace a
  row you filled shows a chip first, with Replace, Add as
  new, and Cancel; empty rows and new rows fill without asking. The `#` grip
  reorders rows before you save. This all needs JavaScript; the text flow
  works without it.
- **Inline media**: On the gist page, images embed inline and audio and
  video get native players. A PDF renders in an iframe, and any other
  binary gets a download link that keeps the file name. The bytes come from
  S3 directly: `/raw` for an attachment answers with a redirect to a
  presigned URL that is valid for ten minutes. The bucket answers the Range
  requests for video seeking, so the app server forwards no file bytes.
- **Sharing**: Every signed-in user can view any gist by URL. Only the owner
  can edit or delete one.
- **List page**: Each row shows the title, the description, and the
  modification time, newest first. Title search runs in the browser over the
  whole list, so typing sends no server request. Timestamps render relative
  ("3 days ago"), with the absolute time in the tooltip. Listing reads one
  JSON object per gist. That is acceptable at personal scale, and it is the
  first thing to optimize if the list grows large. The key listing paginates,
  so the gist count is not capped at 1000.
- **Size limit**: A request body over 16 MiB gets status 413, and the storage
  layer refuses a gist above 16 MiB. Attachments escape both limits: their
  bytes never pass through the app, so their only limit is the bucket itself.

## Configuration

Configuration is a YAML file passed with `-config` (default: `config.yaml`
in the current directory). `configs/example.yaml` documents every key with
values matching the docker-compose dev stack.

Non-secret values live in the file. A secret appears only as a reference:
`oidc.client_secret` names the environment variable that holds the client
secret; if the key is present and that variable is unset, startup fails. S3
credentials are not configuration at all; the standard AWS SDK credential
chain applies (environment variables, shared config file, instance profile).

| Key | Required | Meaning |
| --- | --- | --- |
| `oidc.issuer` | yes | OIDC issuer URL |
| `oidc.client_id` | yes | OAuth client id |
| `oidc.client_secret` | no | Name of the environment variable holding the OAuth client secret |
| `s3.bucket` | yes | Bucket that holds the gists; the app enables its versioning and CORS on boot |
| `s3.endpoint` | no | Custom endpoint, for example `https://sos-de-fra-1.exo.io`. Empty means AWS |
| `listen` | no | Listen address. Defaults to `:8080` |
| `base_url` | no | Externally reachable origin; builds the OAuth redirect URI. Defaults to `http://localhost<listen>` |

`oidc.client_secret` is only needed for the app's own login flow. Without
it, `/login` and `/callback` answer 503 and the app serves only requests
carrying a token from elsewhere — typically an authenticating proxy (for
example oauth2-proxy) that forwards the ID token as an
`Authorization: Bearer` header. Requests without a valid token then answer
401 instead of redirecting to `/login`, which is what a proxy watches for
to trigger re-authentication. Every token is still verified against
`oidc.issuer` and `oidc.client_id`: signature, audience, and expiry. That
is why both stay required. In this mode the app never sets a session
cookie, so the proxy owns the session lifetime entirely; the app only
enforces the token's own `exp` claim.

The app always sends path-style S3 requests and pins the SDK region to
`us-east-1`. S3-compatible providers route by endpoint, so this works.

## Run locally

The compose file starts the same two dependencies the e2e suite uses: s3mock
and Dex with two static users.

1. Start the stack:

   ```
   docker compose up -d
   ```

   Dex listens on `http://localhost:5556/dex` and s3mock on
   `http://localhost:9090` with a bucket named `gists`. The app enables
   versioning on it at startup. s3mock has no CORS API, so the app logs a
   warning about it and continues; s3mock answers every preflight anyway.

2. Copy `configs/example.yaml` to `config.yaml` (the defaults already match
   the stack) and export the client secret the config references:

   ```
   export OIDC_CLIENT_SECRET=secret
   ```

3. Run the app:

   ```
   go run .
   ```

4. Open `http://localhost:8080` and log in as `alice@example.com` or
   `bob@example.com` with the password `password`. Both users live in the
   bcrypt hashes of `dex-config.yaml`. The two users let you test sharing:
   bob can view alice's gists but cannot edit them.

`dex-config.yaml` carries a fixed client secret, test passwords, and
in-memory storage. Do not reuse it in production.

## Tests

Unit tests:

```
go test ./...
```

End-to-end tests against real s3mock and Dex containers (Docker required):

```
go test -tags e2e ./e2e/
```

## Deployment

1. Build: `go build -o dolmen .`
2. Write a config file; `configs/example.yaml` is the starting point. Export
   the environment variables for the client secret and the S3 credentials.
3. Run `./dolmen -config /etc/dolmen/config.yaml` from any directory. The
   binary needs no other file. `./dolmen -version` prints the build stamp.

SIGINT and SIGTERM drain in-flight requests for up to 10 seconds before the
process exits. An unauthenticated `GET /healthz` answers `ok` for container
supervisors.

### Container

A distroless, multi-arch (linux/amd64, linux/arm64) image is published to
ghcr.io: pushes to `main` publish `:latest`, version tags publish
`:vX.Y.Z`.

```bash
docker run -d \
  -v /etc/dolmen/config.yaml:/config/dolmen.yaml:ro \
  -e OIDC_CLIENT_SECRET=... \
  -e AWS_ACCESS_KEY_ID=... -e AWS_SECRET_ACCESS_KEY=... \
  -p 8080:8080 \
  ghcr.io/brutasse/dolmen:latest
```

The image runs as `nonroot` and contains nothing but the binary. The config
file is mounted read-only at the path the entrypoint expects; override the
command arguments to point `-config` elsewhere. S3 credentials come from
the usual AWS environment variables (or any other SDK credential provider).

At startup the app configures the bucket it needs. It enables object
versioning, because gist history is S3 versions. It also ensures a CORS rule
that lets the browser `PUT` attachments directly, with the `Content-Type`
header, from the `base_url` origin. CORS rules written by others stay in
place. These operations need the `s3:GetBucketVersioning`,
`s3:PutBucketVersioning`, `s3:GetBucketCORS`, and `s3:PutBucketCORS`
permissions on the app's credentials. A backend without the CORS API, such
as s3mock, only logs a warning; you must configure attachment CORS yourself
there.

## Storage layout

- `gists/{user}/{gist-id}.json` — the gist: description, files, owner. This
  JSON holds text files inline; an attached file appears as
  `{name, key, mime, size}`. Each save creates a new S3 object version.
- `index/{gist-id}` — a pointer object whose content is the owner id. It
  makes the owner lookup for cross-user views cheap. If the pointer is
  missing, the app finds the owner with a prefix scan and writes the pointer
  back.
- `uploads/{user}/{uuid}` — dropped attachments, one object per file with the
  content type pinned by the server. A gist can reference an upload only
  under the saving user's own prefix, and only if the object exists. A
  replaced attachment or a never-saved upload leaves an orphan object; the
  app never garbage-collects orphans. Clean them with an `uploads/` lifecycle
  rule. A rule shorter than the age of your oldest saved gist would delete
  live data, so set its age with care.

## Assets and theming

The binary embeds everything the UI needs — templates, stylesheet, fonts,
MathJax, Mermaid. There is no build step and no external request:
`go build` is the whole thing.

- `web/assets/app.css` is hand-written. Light and dark themes follow
  `prefers-color-scheme` through CSS custom properties. Edit it directly.
- Fonts are Inter (text) and JetBrains Mono (code): self-hosted variable
  woff2 subsets under `web/assets/fonts/`, each with its OFL license.
- Mermaid 12.1.0 (MIT) and MathJax 3.2.2 (Apache-2.0) are vendored under
  `web/assets/vendor/`, each with its license file.
- The highlight colors in `web/assets/chroma.css` come from the same Go
  style the renderer uses. Regenerate them with:

```
go run ./tools/genchroma > web/assets/chroma.css
```

## Acknowledgements

[Exoscale's On-Demand
Inference](https://www.exoscale.com/ai-cloud-infrastructure/inference/on-demand/)
provided tokens that enabled this app to exist.
