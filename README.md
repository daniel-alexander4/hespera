# Hespera

A local app for your **Music, TV, and Movies**, with automatic metadata
matching. Written in Go: a single static binary that opens a chromeless app
window on your machine (loopback-only — a single-machine app, not a network
server). SQLite storage, server-rendered HTML, no external services required to
run. A headless server mode is also available if you want to reach it from
other devices.

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
`hescli` in `/usr/bin`, an app-menu entry + icon, and pulling the runtime
dependencies (**ffmpeg**, **openssh-client**) via apt. No background service is
installed; launch **Hespera** from your app menu (or run `hespera`) and it opens
an app window:

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
hespera
```

That's it — Hespera opens an app window automatically, bound to a random
loopback port (so it never collides with anything else). On first run, the
window walks you through pointing it at your media folder and adding a library;
you can also set the media folder under **Libraries** or with
`HESPERA_MEDIA_ROOT`. It stores its database, caches, and downloaded artwork in
a per-user data directory (`~/.config/hespera` on Linux, the equivalent on
macOS/Windows).

`HESPERA_NO_BROWSER=1` runs **server mode** instead: no window, binds
`HESPERA_LISTEN` (default `127.0.0.1:8080` — loopback only). To reach it from
other devices, opt in explicitly with `HESPERA_LISTEN=:8080`.

### Security posture

Hespera has **no authentication layer, by design** — it is a single-machine
media app, and in app mode it is only reachable from your own computer.
That means anyone who can reach the port in server mode has *full* access:
not just playback, but the tag editor (writes into your music files), the
integrity auto-repair (the one path that rewrites media files), settings,
and shutdown. The built-in CSRF guard stops hostile web pages, not direct
network peers. Hence the loopback default: exposing Hespera to a network is
an explicit choice, and should only be made on a network you trust end to
end. For anything beyond that (shared LAN, remote access), put a reverse
proxy with authentication in front (Caddy `basic_auth`, nginx `auth_basic`,
Tailscale, etc.) — that is the supported pattern, not an app-level login.

### Performance: sharing a disk with another media server

Hespera runs all of its background work — library scans, integrity checks,
loudness analysis, thumbnail and preview generation — at **idle I/O priority**
(and nice 19), so a long scan yields the disk to anything that needs it right
now, like Plex or Jellyfin streaming a movie from the same drive.

One catch: the kernel only enforces I/O priorities when the disk's scheduler
supports them, and the default on most distros (`mq-deadline`) ignores them.
Check yours (replace `sdb` with your media disk):

```sh
cat /sys/block/sdb/queue/scheduler   # [mq-deadline] → priorities are ignored
```

For a spinning disk (external USB drives especially), switch it to `bfq`:

```sh
# apply now
sudo modprobe bfq
echo bfq | sudo tee /sys/block/sdb/queue/scheduler

# make it stick across reboots
echo bfq | sudo tee /etc/modules-load.d/bfq.conf
echo 'ACTION=="add|change", KERNEL=="sdb", ATTR{queue/scheduler}="bfq"' \
  | sudo tee /etc/udev/rules.d/60-media-disk.rules
```

With `bfq`, Hespera's background jobs still use the disk's full speed when it
is otherwise idle — they only step aside under contention. On NVMe/SSD media
disks this tuning rarely matters; it is for rotational disks shared with
playback.

### Configuration

All configuration is via `HESPERA_`-prefixed environment variables. See the
table in [`CLAUDE.md`](CLAUDE.md#configuration-environment-variables) for the
full list (listen address, data dir, media root, optional API keys for TMDB /
OpenSubtitles / etc., ffmpeg concurrency, HLS cache limits).

