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
./build.sh && ./install.sh
```

Packages and installs the `.deb`s — `hespera` and `hescli` in `/usr/bin` with
an app-menu entry + icon (pulling **ffmpeg** via apt), plus the `hesplay`
music-player client, which ships as its own package so other boxes can install
it without the full Hespera (see [Remote speakers](#remote-speakers-playing-music-on-another-box-hesplay)).
`./install.sh remote [host]` deploys the server .deb to another machine over
ssh; `./install.sh client [host]` deploys just the hesplay .deb. Nothing runs in
the background unless you ask for it (a headless server unit ships, disabled —
see [Serving your household](#serving-your-household)); launch **Hespera** from
your app menu (or run `hespera`) and it opens an app window:

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
make build        # local ./bin/hespera + ./bin/hescli + ./bin/hesplay (quick dev build)
make dist         # cross-compile all platforms + .deb packages into dist/
make install      # build, package, and install on this Debian/Ubuntu machine
make test         # go test ./...
make release      # build + publish dist/ as GitHub release v<VERSION> (needs gh + nfpm)
```

`build.sh` produces one cgo-free static `hespera` binary and one `hesplay`
client binary per OS/arch in `dist/`, plus `.deb` packages for Linux
amd64/arm64 — `hespera_<ver>_<arch>.deb` (server + hescli) and the standalone
`hesplay_<ver>_<arch>.deb` (needs
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
browser), run server mode on the machine that holds the media. The `.deb`
ships a systemd unit for exactly this — it is inert until you enable it:

```sh
echo 'HESPERA_LISTEN=:8080' | sudo tee /etc/default/hespera   # serve the LAN, not just loopback
sudo systemctl enable --now hespera@$USER                     # start now, and at every boot
sudo ufw allow from 192.168.1.0/24 to any port 8080 proto tcp # your LAN subnet
```

The instance name is the account Hespera runs as (`hespera@dan`), so it uses
that user's library, database, and artwork — no root, no linger, and no
`XDG_RUNTIME_DIR` incantation when you come back over SSH. `/etc/default/hespera`
takes any of the environment variables below (`HESPERA_MEDIA_ROOT`,
`HESPERA_DATA_DIR`, …); `sudo systemctl restart hespera@$USER` applies changes,
and `journalctl -u hespera@$USER -f` is the log. Upgrading the package replaces
the unit file, so run `sudo systemctl daemon-reload` before restarting. Not
installing from the `.deb`? Copy `build/hespera@.service` to
`/etc/systemd/system/` yourself — it's the same file.

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

`hesplay` turns any box with speakers — a headless Raspberry Pi in another
room — into a music player for a LAN Hespera. It installs **on its own,
without the full Hespera**: grab the `hesplay_<version>_<arch>.deb` (or the
raw `hesplay-<version>-<os>-<arch>` binary) from the
[releases page](https://github.com/daniel-alexander4/hespera/releases), or
`go build ./cmd/hesplay`. From a repo checkout, `./install.sh client [host]`
pushes the built .deb to a box over ssh and installs it there. It fetches the
same queue the web player uses (so playlists, ordering, and per-track volume
leveling all apply) and plays it through **mpv** (recommended:
`apt install mpv`) or **ffplay** (part of ffmpeg) — the .deb pulls mpv in
when neither is present.

```sh
hesplay server http://plex.local:8080   # save the default server once
                                        # (--server or $HESPERA_SERVER override per call;
                                        #  hesplay server shows it, hesplay server clear forgets it)

hesplay playlists                   # list playlists
hesplay playlist road trip          # play one (names need no quoting)
hesplay album abbey road            # play an album, in track order
hesplay song bohemian rhapsody      # play a single song
hesplay artist queen                # an artist's whole catalog, shuffled
hesplay mix queen                   # a radio mix: that artist + similar artists
hesplay popular                     # the catalog's most popular songs, shuffled
hesplay all                         # the whole catalog, shuffled
hesplay --shuffle album abbey road  # force a shuffle
hesplay --ordered playlist workout  # play a playlist in its curated order
```

An album plays in track order and a song is one track; artist, mix, and playlist
queues shuffle by default (`--ordered` plays them as listed).

**Tab completion.** The .deb wires up bash automatically; otherwise add
`source <(hesplay completion bash)` to your shell rc (`zsh` works too). Verbs
and flags complete offline, and `album`, `artist`, `song`, `mix` and `playlist`
complete **live names from the server** — so `hesplay artist Black<Tab>` lists
the matching artists and fills one in:

```sh
$ hesplay artist Black<Tab>
Black Sabbath   Blackfield   The Black Keys
```

Names complete once two characters are typed (the server's search minimum);
playlists need none, so `hesplay playlist <Tab>` lists them all. Completion
takes one short-timeout request, so a server that's off or unreachable simply
offers nothing rather than hanging the shell. Multi-word names complete a word
at a time and need no quoting.

**Transport keys.** While a queue is playing, single keys move it:

| key | does |
|-----|------|
| `n` | next track (also `.` / `>`) |
| `p` | previous track, or restart the current one once you're more than 10 seconds in — the same idiom as the web players' `\|<` button (also `,` / `<`) |
| `q` | stop and quit |

Ctrl+C still stops as before. Keys need a terminal, so a queue started from a
systemd unit, a cron job, or with redirected input just plays through — there,
stdin is left to the player engine and mpv's own keybindings apply. Linux only
(the terminal handling is per-platform); elsewhere hesplay behaves as it did.

**Phone remote.** `hesplay --listen :8090` serves a small installable web app
from the player itself — open `http://<that-box>:8090` on your phone and Add to
Home Screen. It gives you playlist buttons (play or shuffle), the two quick-play
queues, an A-Z browse through your artists (play or shuffle an artist, open it
for its albums, open an album to pick a song), and once something is playing, a
dozen rows of the queue — the playing song sits among them as a card with its
artwork, a progress bar, and its own previous / pause / next / stop, and any
other row is a tap away from playing.

Pause needs mpv (it goes through the same JSON IPC socket the stall guard uses);
on an ffplay box the control hides itself rather than offering something that
can only fail.

Browsing is done by the player, not the server: Hespera's artist and album pages
are HTML, so hesplay reads the catalog once and indexes it itself. That keeps the
feature working against whatever Hespera version you already run, and the phone
only ever receives the slice it asked for. Artists file under their name with a
leading "The" ignored, so The Rolling Stones sits under R.

```sh
hesplay --listen :8090                     # serve the remote and wait
hesplay --listen :8090 playlist road trip  # …and start playing right away
```

**Leaving it running.** The .deb ships a systemd **template** unit, disabled — so
installing hesplay opens no port until you ask:

```sh
sudo systemctl enable --now hesplay@$USER   # serve the remote on :8090, now and at boot
```

The instance name is the account to run as, and that account needs access to the
sound card (usually: membership of the `audio` group). Override the port in
`/etc/default/hesplay` (`HESPLAY_LISTEN=:9000`); the upstream Hespera comes from
the default that account saved with `hesplay server <url>`.

Enabling it is also what gives the box a **noise schedule** — the reconciler
lives inside the `--listen` process, so a box with no long-running hesplay has
presets but no windows.

**A box running Hespera gets hesplay too.** The hespera package *recommends* it,
which apt installs by default, because hesplay is what plays or generates audio
on that machine's own speakers — and it is what Hespera's home-screen **Noise**
card points at. It is only a recommendation: Hespera is a media server and works
fully without it, and installing it starts nothing.

The music comes out of **that box's** speakers, not the phone's — the phone only
sends the buttons, so locking it, switching apps, or walking out of range stops
nothing. The app's one setting is which Hespera to stream from, and saving it
there is the same as running `hesplay server <url>` on the box.

This is why the remote is an HTTP surface rather than an ssh session to the box:
hesplay traps only SIGINT and SIGTERM, so the SIGHUP that follows a dropped ssh
session would kill playback mid-song — and detaching it to survive that removes
the terminal, which switches the transport keys off entirely. Over a shell,
surviving disconnection and having controls are mutually exclusive.

`--listen` is off unless you pass it, and it has **no authentication**, the same
posture as Hespera itself: anyone who can reach that port can change what is
playing on that box. The blast radius is playback there — it is not a shell and
it cannot touch the library. Bind it on a network you would already trust with
the speakers.

A shuffled catalog sweep — `artist`, `popular`, `all`, and any mix — plays **one
recording per song**: if a track also exists as a live take, a greatest-hits
copy or a remaster, only one of them joins the queue, picked at random, so a
shuffle can't serve you the same song twice in a handful of tracks. Deliberate
orderings keep everything: an album, a playlist, and `--ordered` are untouched.

Names resolve against the server's search — the closest match plays and is
printed. Finished tracks are reported back, so Recently Played and listen
counts include what played upstairs. Ctrl+C stops. The security posture below
applies: `hesplay` talks to the same unauthenticated LAN port as any browser.

**A wedged player can't freeze the queue.** `hesplay` runs one engine process
per track and waits for it, so an engine that neither plays nor exits would
otherwise stall playback indefinitely with no error and no skip — which mpv
does if it hangs probing an audio output the box doesn't actually run. So mpv
is started with a JSON IPC socket and polled: if its playback position stops
advancing (or never starts) for 20 seconds, the process is killed — SIGKILL,
since a wedged mpv ignores SIGTERM — and the track is skipped with a warning.
A skipped track is never reported as played, so listen counts stay honest.
ffplay has no IPC and plays unguarded.

**Ambient noise.** `hesplay noise` generates ambient noise on that box's
speakers — a sleep aid for a bedroom Pi, and a replacement for the SoX one-liner
people usually wrap in a systemd unit. Seven colours ship: **brown, pink, white,
tpdf, blue, violet, velvet**. It needs the **`sox`** package, and the last three
additionally need **`ffmpeg`** (see below); `hesplay noise` names whichever is
missing rather than failing obscurely.

```sh
hesplay noise                     # the default preset, until Ctrl+C
hesplay noise pink                # a named preset
hesplay noise --for 45 brown      # a sleep timer, in minutes
hesplay noise list                # the presets and their tuning
hesplay noise --print             # the exact SoX command, without playing it
```

The sound is not flat hiss: the noise is band-passed and then modulated very
slowly, so it swells and fades like surf rather than sitting at one level. Each
preset carries its own colour, band centre and width, swell rate and depth,
reverb, gain and fade-in. A preset's internal buffer length is derived from the
swell rate rather than configured, so the loop is always a whole number of
swells and never jumps mid-swell.

**Where the colours come from.** SoX synthesizes brown, pink, white and tpdf
itself. Blue, violet and velvet do not exist in SoX at any version, so those are
generated by ffmpeg's `anoisesrc` and **piped into the same SoX effect chain** —
`hesplay noise --print blue` shows the pipeline. This is not a contradiction of
using SoX in the first place: that choice turned on the 0.08 Hz swell, which
ffmpeg's `tremolo` filter cannot go below 0.1 Hz to reach, and in the hybrid the
tremolo still runs in SoX. ffmpeg only supplies raw noise, for colours SoX
cannot make at all.

**The presets are level-matched, by measurement.** Run through the same
band-pass the colours land up to 20 dB apart — violet is 20.6 dB quieter than
brown — so shipping them all at unity would make switching to violet sound like
the noise had stopped. Each preset carries a gain bringing it to brown's
measured level, with brown itself at unity so it stays exactly the original
command. Note that equal RMS is not equal *loudness*: the ear is most sensitive
around 2–4 kHz, so blue and violet still read as somewhat louder than brown
despite matching on paper. Trim by ear with the gain knob.

**A schedule, reconciled rather than triggered.** Windows live in the same
config and are checked every 30 seconds — "should noise be playing right now?" —
instead of being armed as timers. That is the whole reason to prefer this over
systemd timers: a box rebooted at 23:00, inside a 20:00–10:00 window, starts
making noise again on the next check rather than staying silent until the
following evening. Windows may name days, and a window whose end is earlier than
its start runs overnight; days name the day it *starts* on.

Noise and music take turns rather than mixing — the box has one sound card, and
on a board with no audio server the second thing to open it simply fails. So
starting music stops the noise, and when the music ends the noise comes back on
its own if the window is still open. Starting noise likewise stops the music.
Pressing **stop** during a scheduled window is treated as "not tonight" and
suppresses the rest of that window, so the schedule does not immediately undo
you; the next window starts normally.

Everything is edited from the phone remote's **Noise** screen: preset tuning,
the weekly schedule, and one tap per preset to start it now. Setting **Noise
remote** in Hespera's Settings → Features to `http://<that-box>:8090` adds a
**Noise** card to Hespera's home screen that links there. Hespera never contacts
that address itself — the card is a plain link your browser follows — so pointing
it somewhere is not a way to make the server fetch anything.

> One limitation worth knowing: SoX exposes no control socket, so noise gets no
> equivalent of the stall guard above. A noise process that *exits* is noticed
> and restarted on the next check; one that hangs while still running is not.

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

### Power button: shutting down an appliance box

On a TV box with no keyboard, the only way to stop the machine is usually to
pull the plug — which is how filesystems end up replaying journals and
external drives collect unclean power cycles. Hespera can show a power button
on the home screen instead, which shuts the machine down through systemd:
the service stops, the database is flushed, and your library filesystem is
unmounted cleanly.

It is **off by default**, and deliberately awkward to turn on, because the
same binary runs household servers and desktop app windows where halting the
host would be destructive. To enable it:

1. Turn on **Settings → Features → "Show a power button on the home screen"**
   (or `hescli config set power_button_enabled 1`).
2. Grant the user Hespera runs as permission to power off the machine, by
   creating `/etc/polkit-1/rules.d/50-hespera-poweroff.rules`:

   ```javascript
   polkit.addRule(function (action, subject) {
     if ((action.id == "org.freedesktop.login1.power-off" ||
          action.id == "org.freedesktop.login1.power-off-multiple-sessions") &&
         subject.user == "YOUR_USERNAME") {
       return polkit.Result.YES;
     }
   });
   ```

   Both action ids are needed, not just the first: logind escalates to the
   `-multiple-sessions` variant whenever another session is open, and on an
   appliance box — a kiosk on tty1, plus any SSH login — that is the normal
   state rather than the exception. Note also that the service runs with no
   active login session, so the rule has to return `YES` outright; a rule
   relying on polkit's "active session" path will never match it.

That polkit rule is **not** shipped in the package on purpose: installing a
rule that lets a web UI halt its host would be a silent privilege escalation
on every machine Hespera is installed on. Granting it is your decision, per
machine. Without it the button still appears, but reports an error instead of
powering off.

Two safeguards apply regardless of the setting: the button is shown and
accepted **only in a browser on the machine itself** (other devices on your
network never see it, and their requests are refused), and it always asks for
confirmation first — a remote's arrow keys pass through the home screen's
utility row, and a single stray press should not end playback for everyone.

### Screen mode: when a TV shows nothing, or the wrong size

A computer and a TV agree on a video mode when they're plugged together. If
the TV was switched off, or on another input, at the moment the machine
booted, they never had that conversation — so you get no picture at all, or a
picture at some resolution the TV would rather not use. Until now the only
cure was editing the kernel command line on the boot partition and rebooting.

Hespera can pick the mode instead, from **Settings → Features → "Let Hespera
set this machine's screen mode"** (off by default). Choose a resolution and
refresh rate, and it applies at once — then asks whether you can still see
anything. If you don't confirm within fifteen seconds it puts the old mode
back on its own, so a mode your TV can't display fixes itself rather than
leaving you with a black screen and no way to undo it. A mode you *do*
confirm is remembered and re-applied every time Hespera starts, so it
survives reboots.

Requirements and limits, in one place:

- **Linux running X11 only.** It uses `xrandr`, from the `x11-xserver-utils`
  package. On macOS and Windows the desktop owns display configuration and
  the setting doesn't appear.
- **If Hespera runs as a background service**, it has no display session of
  its own, so it can't reach the screen until you tell it which one:
  add `DISPLAY=:0` to `/etc/default/hespera` and restart the service. (If
  your X session keeps its authority file somewhere unusual, add
  `XAUTHORITY=/path/to/.Xauthority` too.) Until you do, the setting says so
  rather than offering a control that quietly does nothing.
- Like the power button, it is shown and accepted **only in a browser on the
  machine itself** — other devices on your network never see it.
- `hescli config set display_mode "HDMI-1 1920x1080 60.00"` sets it from an
  SSH session, which is the way in when there's nothing on the screen to look
  at. That takes effect at the next restart.
- It can only choose among modes the screen tells the computer it supports.
  If the connector reports **nothing attached** — a TV that was off at boot,
  showing no EDID at all — there is no list to choose from, and the fix is
  still a forced mode on the kernel command line (`video=HDMI-A-1:1920x1080@60D`
  in `/boot/firmware/cmdline.txt` on a Raspberry Pi) or a saved EDID file.
  Kill switch: `HESPERA_NO_DISPLAY_CONTROL=1`.

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

