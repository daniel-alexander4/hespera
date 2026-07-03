# Third-Party Licenses

Hespera itself is licensed under the GNU General Public License v3.0 (see
[`LICENSE`](LICENSE)). It uses the third-party software listed below, each
distributed under its own license, retained here as required. Every license
named here (MIT, BSD-2/3-Clause, the SQLite public-domain blessing) is
compatible with redistribution under GPLv3.

The **verbatim license text** of every module below is committed under
[`third_party/licenses/`](third_party/licenses/) (one directory per module
path), embedded into the binaries (`embed.go`) and served at
`/about/licenses`, and shipped in the `.deb` under
`/usr/share/doc/hespera/licenses`. `TestThirdPartyLicenseTexts` fails the
build when a go.mod module has no text there; regeneration instructions are
in that directory's README.

> This file is checked against `go.mod` by a test
> (`TestThirdPartyLicensesCurrent`): every module in `go.mod`'s `require`
> blocks — **direct and indirect** (i.e. everything that compiles into the
> binary) — must be listed here or the build fails, so the attribution can't
> silently drift. The check verifies *presence* of each module path, not the
> verbatim license text.

## Go modules — direct dependencies

- **github.com/bogem/id3v2/v2** — MIT License.
- **github.com/dhowden/tag** — BSD-2-Clause License.
- **github.com/gcottom/audiometa/v3** — MIT License (Copyright © 2024 Gage Cottom). The published module omits the `LICENSE` file; the license is in the upstream repository: <https://github.com/gcottom/audiometa/blob/main/LICENSE.md>.
- **modernc.org/sqlite** — BSD-3-Clause License. The pure-Go translation of the SQLite amalgamation it bundles is itself in the public domain (the SQLite blessing; see the module's `SQLITE-LICENSE`).

## Go modules — indirect dependencies (compiled into the binary)

These transitive modules link into the `hespera` binary (the `CGO_ENABLED=0`
compile closure of `./cmd/hespera`); they are not in the binary by way of a
direct import in this repo, but their code ships with it.

- **github.com/abema/go-mp4** — MIT License.
- **github.com/aler9/writerseeker** — MIT License.
- **github.com/dustin/go-humanize** — MIT License.
- **github.com/gcottom/flacmeta** — MIT License. (Its `LICENSE.md` also reproduces the Apache License 2.0 covering a third-party component it embeds.)
- **github.com/gcottom/mp3meta** — MIT License.
- **github.com/gcottom/mp4meta** — MIT License.
- **github.com/gcottom/oggmeta** — MIT License.
- **github.com/google/uuid** — BSD-3-Clause License.
- **github.com/mattn/go-isatty** — MIT License.
- **github.com/ncruces/go-strftime** — MIT License.
- **github.com/remyoudompheng/bigfft** — BSD-3-Clause License.
- **github.com/sunfish-shogi/bufseekio** — MIT License.
- **golang.org/x/sys** — BSD-3-Clause License.
- **golang.org/x/text** — BSD-3-Clause License.
- **modernc.org/libc** — BSD-3-Clause License.
- **modernc.org/mathutil** — BSD-3-Clause License.
- **modernc.org/memory** — BSD-3-Clause License.

The full license text of each module lives in its source tree (in the module
cache under `$GOPATH/pkg/mod/<module>@<version>/`, and in each project's
repository). Verbatim per-module license **texts** are not yet bundled into
this distribution — they matter for a public *binary* release and are tracked
as a residual; source/peer-to-peer distribution carries each module's terms via
the modules themselves.

## Vendored web assets

- **Hotwire Turbo** (`web/static/turbo.umd.js`) — MIT License. © 37signals / Basecamp.
- **hls.js** (`web/static/hls.light.min.js`) — Apache License 2.0. © the hls.js authors (video-dev). The minified bundle carries no in-file header banner; its attribution is retained here.
- **Catppuccin** palette (color values in `web/static/app.css`) — MIT License. © Catppuccin.
- **Lucide** icons (inlined SVGs in `web/templates/partials_icons.html`) — ISC License. © Lucide Contributors.
