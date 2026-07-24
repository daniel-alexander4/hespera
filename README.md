# Hespera

A local app for your **Music, TV, Movies, Photos, Books, and Audiobooks**,
with automatic metadata matching. Written in Go: a single static binary that opens a chromeless app
window on your machine (loopback-only — a single-machine app, not a network
server). SQLite storage, server-rendered HTML, no external services required to
run. A headless server mode is also available if you want to reach it from
other devices.

Licensed under the [GNU AGPL v3](LICENSE); third-party attributions in
[`THIRD_PARTY_LICENSES.md`](THIRD_PARTY_LICENSES.md).

## Features

- **Music** — MusicBrainz matching with Cover Art Archive covers, artist bios
  and images, synced karaoke-style lyrics (LRCLIB), persistent playlists and
  one-click instant mixes, shuffle by era or popularity, duplicate detection,
  and a per-track tag editor that writes back to your files.
- **TV & Movies** — TMDB matching (posters, backdrops, cast, collections,
  related titles), direct play when your browser can handle the file and
  **seekable on-demand HLS transcoding** when it can't, embedded +
  on-demand OpenSubtitles subtitles, **skip-intro detected by audio
  fingerprinting**, scrub-preview thumbnails, per-episode screen-capture
  thumbnails, Up Next auto-advance, and watched/resume tracking.
- **Photos** — a capture-date timeline built from EXIF (with a folders view
  and year filters); home-video clips play through the same engine as TV.
- **Books** — EPUB, CBZ comics, and PDF in an in-app reader with covers,
  embedded metadata, and per-book resume; nothing to configure and no
  external services (metadata comes from the files themselves).
- **Audiobooks** — chaptered m4b (and plain audio) with embedded covers,
  chapter skipping, variable speed, and resume to the second, played through
  the same transport as TV and movies.
- **Library care** — a filesystem watcher auto-scans new media, corruption
  detection with lossless container auto-repair, loudness leveling, and jobs
  interrupted by a shutdown resume automatically on the next launch.
- **Couch-friendly** — the whole UI drives with arrow keys or a TV remote,
  scales itself to the physical display size, and honors hardware media keys.
- **Local-first** — one binary, SQLite, your files stay yours: no accounts,
  no telemetry, and external services are used only to *fetch* metadata.

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
make release      # build + publish dist/ as GitHub release v<VERSION> (needs gh + nfpm)
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
loopback port (so it never collides with anything else). The window is a
Chromium-family app-mode window (Chrome, Chromium, Edge, or Brave — on Linux
one of these must be installed; Hespera deliberately won't hand the app
window to a non-Chromium default browser). On first run, the
window walks you through pointing it at your media folder and adding a library;
you can also set the media folder under **Libraries** or with
`HESPERA_MEDIA_ROOT`. It stores its database, caches, and downloaded artwork in
a per-user data directory (`~/.config/hespera` on Linux, the equivalent on
macOS/Windows).

`HESPERA_NO_BROWSER=1` runs **server mode** instead: no window, binds
`HESPERA_LISTEN` (default `127.0.0.1:8080` — loopback only). To reach it from
other devices, opt in explicitly with `HESPERA_LISTEN=:8080`.

**Focus-follows-mouse desktops.** If your window manager focuses whatever the
pointer hovers (Cinnamon's `focus-mode='mouse'`, and the sloppy-focus variants),
a newly opened window on *another* monitor never gets the keyboard — the WM
hands focus to whatever sits under the pointer. Install `xdotool` and Hespera
will move the pointer onto its own window at launch so the window manager gives
it focus (it does nothing when the pointer is already over the window, and
nothing at all on click-to-focus desktops). `HESPERA_NO_FOCUS_STEAL=1` turns it
off.

### Serving your household

To let other devices in the house use Hespera (phones, laptops, a TV
browser), run server mode on the machine that holds the media, started at
boot by a systemd **user** unit:

```ini
# ~/.config/systemd/user/hespera.service
[Unit]
Description=Hespera media server

[Service]
Environment=HESPERA_NO_BROWSER=1
Environment=HESPERA_LISTEN=:8080
ExecStart=/usr/bin/hespera
Restart=on-failure

[Install]
WantedBy=default.target
```

```sh
systemctl --user daemon-reload && systemctl --user enable --now hespera
loginctl enable-linger $USER          # keep it running when logged out
sudo ufw allow from 192.168.1.0/24 to any port 8080 proto tcp   # your LAN subnet
```

Devices then browse `http://<hostname>:8080`. Phones and laptops get the
right UI scale automatically; a TV browser can pin the 10-foot scale once
with `?scale=tv` (it persists per browser). On the server's own screen,
**just launch Hespera from the app menu as usual** — when a running instance
is detected (the service), the launcher *attaches*: it opens the same
chromeless app window onto it instead of starting a second copy, and exits.
Stop the service and the icon goes back to launching a standalone app.

Notes for shared use: Hespera has one household-wide state — watched marks,
resume positions, and playlists are shared by everyone (there are no user
profiles); the security posture below applies (trusted network only); and
there is no shutdown control in the UI, so a phone can't stop the family
server (quitting is closing the app window, or stopping the service).

### Remote speakers: playing music on another box (`hesplay`)

`hesplay` (installed alongside `hespera`/`hescli`, or `go build ./cmd/hesplay`)
turns any Linux box with speakers — a headless Raspberry Pi in another room —
into a music player for a LAN Hespera. It fetches the same queue the web
player uses (so playlists, ordering, and per-track volume leveling all apply)
and plays it through **mpv** (recommended: `apt install mpv`) or **ffplay**
(part of the ffmpeg the .deb already installs).

```sh
export HESPERA_SERVER=http://plex.local:8080   # or --server per call

hesplay playlists                   # list playlists
hesplay playlist road trip          # play one (names need no quoting)
hesplay album abbey road            # play an album, in track order
hesplay artist queen                # an artist's whole catalog, shuffled
hesplay mix queen                   # a radio mix: that artist + similar artists
hesplay popular                     # the catalog's most popular songs, shuffled
hesplay all                         # the whole catalog, shuffled
hesplay --shuffle album abbey road  # force a shuffle
hesplay --ordered playlist workout  # play a playlist in its curated order
```

An album plays in track order; artist, mix, and playlist queues shuffle by
default (`--ordered` plays them as listed).

Names resolve against the server's search — the closest match plays and is
printed. Finished tracks are reported back, so Recently Played and listen
counts include what played upstairs. Ctrl+C stops. The security posture below
applies: `hesplay` talks to the same unauthenticated LAN port as any browser.

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

### Remote control: your desktop may be eating fast-forward and rewind

Hespera honors the hardware media keys a TV remote sends through an IR receiver
(a Flirc, say): play/pause, fast-forward, rewind, next, previous. On a Linux
desktop, though, **fast-forward and rewind may never reach it** — and the cause
is upstream of Hespera.

Cinnamon and GNOME grab the media keysyms globally, then re-dispatch them to the
browser over MPRIS. That works for play/pause, next and previous, which have
MPRIS verbs. **MPRIS has no fast-forward or rewind verb**, so the desktop grabs
those two keys, finds nothing to forward them to, and the press dies there: the
browser never sees it, as a media action *or* a keystroke. The symptom is
distinctive — play/pause works, FF/RW do nothing at all.

The fix is to release the two keys so they fall through to the focused window,
which is all Hespera needs (it already handles them):

```sh
# Cinnamon
gsettings set org.cinnamon.desktop.keybindings.media-keys audio-forward "[]"
gsettings set org.cinnamon.desktop.keybindings.media-keys audio-rewind  "[]"

# GNOME
gsettings set org.gnome.settings-daemon.plugins.media-keys seek-forward  "[]"
gsettings set org.gnome.settings-daemon.plugins.media-keys seek-backward "[]"
```

This only gives up a desktop-wide FF/RW shortcut that, for MPRIS players like a
browser, was doing nothing in the first place. Leave the play/pause binding
alone — that one is *how* play/pause reaches Hespera.

### Configuration

Day-to-day settings (media folder, API keys, feature toggles, subtitle
defaults) live in the in-app **Settings** page. Environment variables cover
the rest — all `HESPERA_`-prefixed:

> **Bundled provider keys.** Official release binaries ship with bundled keys
> for TMDB, fanart.tv, and OpenSubtitles, so a fresh download matches TV/Movies
> and searches subtitles with no setup. A key you enter in Settings (or via the
> env vars below) always overrides the bundled one. Binaries you build from
> source carry no bundled keys — supply your own to enable those features.

| Variable | Default | Purpose |
|----------|---------|---------|
| `HESPERA_NO_BROWSER` | (unset) | Set → **server mode**: no app window, honors `HESPERA_LISTEN`. Unset → app mode (chromeless window on a random loopback port) |
| `HESPERA_LISTEN` | `127.0.0.1:8080` | Server-mode listen address — loopback by default; LAN exposure is an explicit opt-in (`:8080`) |
| `HESPERA_DATA_DIR` | per-user config dir | Database, caches, artwork |
| `HESPERA_DB_PATH` | `<data dir>/hespera.sqlite` | Database path |
| `HESPERA_MEDIA_ROOT` | home dir | Media root (the path-containment boundary; also settable in Settings → Libraries) |
| `HESPERA_TMDB_API_KEY` | bundled | TMDB key for TV/movie matching. Official releases ship a bundled key — set your own only for a dedicated key (also settable in Settings) |
| `HESPERA_FANARTTV_API_KEY` | bundled | fanart.tv key — artist image backfill. Releases ship a shared project key; set your own **personal** key for faster new-artwork access |
| `HESPERA_THEAUDIODB_API_KEY` | | Optional TheAudioDB key — artist bio/image backfill (user-supplied) |
| `HESPERA_LASTFM_API_KEY` | | Optional Last.fm key — popularity blend for shuffles (user-supplied) |
| `HESPERA_OPENSUBTITLES_API_KEY` | bundled | OpenSubtitles consumer key — on-demand subtitle search. Releases ship a bundled consumer key (5 downloads/day per IP) |
| `HESPERA_OPENSUBTITLES_USER_AGENT` | `Hespera v1.0` | OpenSubtitles consumer UA, formatted `AppName vX.Y` |
| `HESPERA_LOG_LEVEL` | `info` | Log level (`debug`/`info`/`warn`/`error`). Per-request access logs are emitted at `debug`; the default `info` keeps request serving off the log path. Set `debug` to see per-request logs |
| `HESPERA_FFMPEG_CONCURRENCY` | 4 | Max concurrent ffmpeg/ffprobe processes |
| `HESPERA_HLS_ENCODER` | `software` | HLS video encoder: `software` (libx264) or `vaapi` (opt-in hardware encode) |
| `HESPERA_HLS_SEGMENT_CONCURRENCY` | 1 | Max concurrent HLS segment transcodes (keeps prefetch bursts off every core) |
| `HESPERA_FFMPEG_ACQUIRE_TIMEOUT` | 2s | How long foreground ffmpeg work waits for a slot |
| `HESPERA_TV_HLS_CACHE_MAX_BYTES` | 20GiB | HLS transcode cache budget |
| `HESPERA_TV_CACHE_MAX_AGE` | 72h | HLS cache entry max age |
| `HESPERA_TRICKPLAY_CACHE_MAX_BYTES` | 10GiB | Scrub-preview sprite cache budget |

