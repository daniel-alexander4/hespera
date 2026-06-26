# Couch Mode (TV + remote)

Couch mode turns the normal Hespera web UI into a 10-foot experience you can
drive from the sofa with a remote control — for a Raspberry Pi or laptop wired
to a TV over HDMI. It is **purely a front-end layer**: the same pages, restyled
large and dark, made navigable by arrow keys with a visible focus ring. There is
no separate server mode and no new playback state.

## How it works

- A bootstrap in `layout.html` sets `<html data-couch="1">` when the page is
  loaded with `?couch=1` (the choice is remembered in `localStorage`; load any
  page with `?couch=0` to turn it off again).
- `web/static/tv.css` re-themes the existing pages under that attribute: hides
  the top nav and footer, scales the rem-based UI up, and adds a high-contrast
  focus ring.
- `web/static/couch.js` provides remote/keyboard navigation: arrow keys move
  focus geometrically (tracking the row/column so focus doesn't drift
  diagonally on dense grids), **Enter/OK** activates the focused link or button,
  and **Backspace/Escape** goes back. When an overlay is open (anything tagged
  `[data-couch-overlay]`, e.g. the player's playlist modal), arrows stay trapped
  inside it and Back dismisses it — via its `[data-couch-dismiss]` control —
  instead of leaving the page.
- The player wires the browser **Media Session API**, so a remote's dedicated
  play/pause/next/previous keys — and the TV/OS now-playing widget — control
  playback.

Navigate **Home → Music → artist → album → play** entirely with the remote.
TV and Movies surfaces inherit the same layer automatically as those libraries
land.

## The remote

Couch mode listens for **standard key events**, so it is remote-agnostic. Use
any remote that emits keycodes:

- A **Flirc USB dongle** programmed to map a cheap IR remote's D-pad to the
  arrow keys, Enter, and Backspace (the usual media-center setup), or
- A **Bluetooth media remote** (most emit arrow keys + the media keys directly).

> The TV's *own* remote over HDMI-CEC is not wired here — that needs a host-side
> `cec → keyboard` bridge and is tracked separately on the pending list.

## Running the couch appliance

Hespera's auth is SSH-pubkey challenge-response with a 24h session — awkward for
an unattended TV box. For a single-purpose device where Hespera and the browser
run on the same machine, run that instance with auth off and bound to loopback so
only the box itself can reach it:

```bash
HESPERA_LISTEN=127.0.0.1:8080 \
AUTH_ENABLED=false \
hespera
```

Then launch a fullscreen kiosk browser pointed at couch mode:

```bash
chromium --kiosk --noerrdialogs --disable-infobars \
  --autoplay-policy=no-user-gesture-required \
  "http://127.0.0.1:8080/?couch=1"
```

Disable screen blanking so the TV doesn't sleep mid-playback:

```bash
xset s off -dpms          # X11
# or, on Wayland, use your compositor's idle-inhibit setting
```

## Autostart

A sample `systemd --user` unit is committed at
[`docs/hespera-kiosk.service`](./hespera-kiosk.service) — the portable choice
across X11, Wayland, and laptop distros. Install it per-user:

```bash
mkdir -p ~/.config/systemd/user
cp docs/hespera-kiosk.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now hespera-kiosk.service
loginctl enable-linger "$USER"   # start at boot without an interactive login
```

Edit the unit's `ExecStart`/paths to match your device before enabling.
