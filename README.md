# Hespera

A self-hosted media server for **Music, TV, and Movies**, with automatic
metadata matching. Written in Go: a single static binary serves a web app on
`:8080` that you stream from any device on your network. SQLite storage,
server-rendered HTML, no external services required to run.

Licensed under the [GNU GPL v3](LICENSE); third-party attributions in
[`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md).

## Install

Hespera ships as a self-contained binary — the web UI assets are embedded, so
there is no directory to deploy alongside it. Builds are available for **Linux,
macOS, and Windows** (amd64 + arm64).

### Debian / Ubuntu (recommended)

```sh
./install.sh
```

Builds Hespera, packages a `.deb`, and installs it — placing `hespera` and
`hescli` in `/usr/bin` and pulling the runtime dependencies (**ffmpeg**,
**openssh-client**) via apt. No background service is installed; start the
server yourself:

```sh
hespera
```

### Other platforms (macOS, Windows, other Linux)

Build the binaries (or grab one from `dist/` after `./build.sh`) and run the
`hespera` binary directly — it's fully self-contained. **ffmpeg must be on your
PATH** for TV/movie playback (transcoding and tag recovery); music and metadata
work without it.

- macOS: `brew install ffmpeg`
- Windows: `winget install ffmpeg` (or `choco install ffmpeg`)

```sh
./hespera-<version>-<os>-<arch>
```

## Build from source

Requires Go 1.23+.

```sh
make build        # local ./bin/hespera + ./bin/hescli (quick dev build)
make dist         # cross-compile all platforms + .deb packages into dist/
make install      # build, package, and install on this Debian/Ubuntu machine
make test         # go test ./...
```

`build.sh` produces one cgo-free static `hespera` binary per OS/arch in `dist/`,
plus `.deb` packages for Linux amd64/arm64 (needs
[`nfpm`](https://github.com/goreleaser/nfpm): `go install
github.com/goreleaser/nfpm/v2/cmd/nfpm@latest`).

## Run

```sh
HESPERA_MEDIA_ROOT=/path/to/your/media \
AUTH_ENABLED=false \
hespera
```

Then open <http://localhost:8080>.

By default the server stores its database, caches, and downloaded artwork in a
per-user data directory (`~/.config/hespera` on Linux, the equivalent on
macOS/Windows). Point `HESPERA_MEDIA_ROOT` at your media library — there is no
universal default, so it falls back to your home directory until you set it.

### Authentication

`AUTH_ENABLED` defaults to **true**, which requires an `AUTH_SESSION_SECRET`
(16+ chars) and `ssh-keygen` on PATH for SSH-pubkey login. For a simple
trusted-LAN setup, disable it:

```sh
AUTH_ENABLED=false hespera
```

### Configuration

All configuration is via `HESPERA_`-prefixed environment variables. See the
table in [`CLAUDE.md`](CLAUDE.md#configuration-environment-variables) for the
full list (listen address, data dir, media root, optional API keys for TMDB /
OpenSubtitles / etc., ffmpeg concurrency, HLS cache limits).

## Docker

A `Dockerfile` and `docker-compose.yml` are still provided if you prefer a
container (the image bundles ffmpeg):

```sh
docker compose up --build
```
