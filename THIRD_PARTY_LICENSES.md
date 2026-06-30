# Third-Party Licenses

Hespera itself is licensed under the GNU General Public License v3.0 (see
[`LICENSE`](LICENSE)). It uses the third-party software listed below, each
distributed under its own license, retained here as required.

> This file is checked against `go.mod` by a test
> (`TestThirdPartyLicensesCurrent`): a new **direct** dependency must be added
> here or the build fails, so the attribution can't silently drift.

## Go modules (direct dependencies)

- **github.com/bogem/id3v2/v2** — MIT License.
- **github.com/dhowden/tag** — BSD (3-clause) License.
- **github.com/gcottom/audiometa/v3** — MIT License (Copyright © 2024 Gage Cottom). The published module omits the `LICENSE` file; the license is in the upstream repository: <https://github.com/gcottom/audiometa/blob/main/LICENSE.md>.
- **modernc.org/sqlite** — BSD-3-Clause License. The pure-Go translation of the SQLite amalgamation it bundles is itself in the public domain (the SQLite blessing; see the module's `SQLITE-LICENSE`).

Transitive (indirect) dependencies are distributed under their own licenses; see
`go.sum` and each module's repository.

## Vendored web assets

- **Hotwire Turbo** (`web/static/turbo.umd.js`) — MIT License. © 37signals / Basecamp.
- **hls.js** (`web/static/hls.light.min.js`) — Apache License 2.0. © the hls.js authors (video-dev). The minified bundle carries no in-file header banner; its attribution is retained here.
- **Catppuccin** palette (color values in `web/static/app.css`) — MIT License. © Catppuccin.
- **Lucide** icons (inlined SVGs in `web/templates/partials_icons.html`) — ISC License. © Lucide Contributors.
