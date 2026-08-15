# PLAN — Hespera on a Roku TV

**Status: planning.** This document is the single source of truth for the Roku
work. Where this plan and the original brief (the hand-off "Get this app running
on a Roku TV") differ, **the plan wins** — the brief's Task 0 asked for an
assessment before any code, and this is that assessment carried through into a
build order.

This plan is scaffolding. It retires into `CLAUDE.md` + ADRs once built
(STANDARDS §15.6); until then, amendments accumulate here.

**Dateline.** Seeded 2026-08-15 from the Roku hand-off brief + a code and
external-reality recon (D1–D14, build order P00–P06).

---

## The assessment (the brief's Task 0, answered)

**What this app is.** Hespera is a Go 1.23 media server — stdlib
`http.ServeMux`, SQLite (WAL, `modernc.org/sqlite`), server-rendered
`html/template` pages with vanilla CSS/JS, `web/` embedded via `//go:embed`.
Entry point `cmd/hespera/main.go`; seven direct dependencies; ffmpeg/ffprobe are
the one external runtime tool. It ships as self-contained binaries and `.deb`s.

**Existing Roku target: none.** Verified — no `manifest`, no `.brs`, no
`components/`, no Roku CI anywhere in the tree. The only occurrences of the word
are a UX comment in `web/static/media_player.js` (the DVR-scan speed tiers are
modelled on Roku's) and `PREDECESSOR_KNOWLEDGE.md:41`.

**Classification: (b) viable, and it is a client rewrite — not a port.** Roku
executes BrightScript + SceneGraph only. There is no browser, no WebView, no JS
runtime, so **none of `web/` runs on a Roku** and none of it can be made to. What
*does* transfer is everything below the templates: Hespera is already a media
server that speaks JSON to non-browser clients (`hescli` over a unix socket,
`hesplay` over plain HTTP). A Roku channel is a fifth client of the same server,
and the server keeps running where it runs today.

The work therefore splits in two, and the second half is smaller than it looks:

- **Server-side (Go).** Browse data is HTML-only today; a Roku needs JSON.
  This is real work — but it is additive, and it reuses the existing queries.
- **Playback (Go).** Nearly free. `/tv/playback-session` already returns a
  complete client-agnostic contract (decision, protocol, URL, audio/subtitle
  tracks, chapters, skip segments, resume position, `stream_start_seconds`), and
  `handlers_stream_tv.go:368` already reads a `?client=` hint into
  `playback.Profile`. The predecessor shipped a `roku-generic` row in that same
  table (`PREDECESSOR_KNOWLEDGE.md:41`); Hespera kept the seam and dropped the
  row. Playback progress is already a JSON POST (`handlers_tv.go:2010`), and its
  earn-only `completed=MAX(...)` rule is server-side — so a Roku client inherits
  watched-flag correctness for free.
- **Channel-side (BrightScript/SceneGraph).** A genuine from-scratch 10-foot UI:
  grids, detail screens, a video screen, a settings/connect screen. This is the
  bulk of the hours and none of it is portable from `web/`.

**Honest estimate.** Server-side JSON + profile work: ~1 phase (P01), the
smaller half. The channel itself: 4 phases (P02–P06), because every screen is
new code in a language nothing else in the tree uses. Nothing here is
infeasible, and nothing needs a capability Roku lacks — *for the six verticals
in scope*. Books are the exception and are cut (D11).

---

## Repo laws (the invariants this work may not break)

- **L1 — Direct play or say so.** The target is a 1080p Roku TV served by the
  Pi 5 (`hespera`, 192.168.1.189), which cannot sustain concurrent transcodes.
  The channel must never *silently* trigger one. Roku's decode set is wider than
  Chrome's — MKV/MP4/MOV, H.264/HEVC/AV1, AAC/MP3/FLAC/ALAC + AC3/E-AC3/DTS
  passthrough — so the correct posture is to exploit that and surface the
  decision when it can't be met. A transcode is a visible outcome, never a
  default.
- **L2 — The TV is a viewer, not an administrator.** The channel gets read
  access plus playback-progress writes. No scan, no match approval, no library
  delete, no settings, no `/poweroff` (already loopback-gated), no tag editing.
- **L3 — The client contract is declared, not incidental.** JSON responses
  marshal named DTO structs with a guard test pinning the field set — never a
  template view model. A template refactor must not be able to silently break
  the TV.
- **L4 — No new Go dependency, and no non-BrightScript runtime on the device.**
  The channel is BrightScript + SceneGraph XML; the server side is stdlib.
- **L5 — Nothing may hardcode this house.** No baked-in IPs, no dev-only
  assets, no assumption of a single server. Keeps the Beta/Channel Store path
  open without a rewrite (D12).

---

## Decisions

### D1 — The Roku channel is a native client, not a UI port *(settled 2026-08-15, auto-adopted: external reality)*
Roku runs BrightScript + SceneGraph and nothing else — no browser, no WebView,
no JS. Hespera's UI cannot be displayed on it at any fidelity. The channel is a
new front end over the existing HTTP API; the server is unchanged in kind.
*Provenance:* [Roku streaming specs](https://developer.roku.com/dev/docs/media),
[SceneGraph Video node](https://developer.roku.com/docs/references/scenegraph/media-playback-nodes/video.md).

### D2 — The channel source lives in this repo *(settled 2026-08-15, auto-adopted: house precedent)*
`roku/` at the repo root (`roku/manifest`, `roku/source/`, `roku/components/`,
`roku/images/`), built to `dist/hespera-roku.zip`. The JSON contract and its
consumer must version together — a separate repo makes the contract a
cross-repo integration with no guard. Precedent: `hesplay` is a separate binary
and a separate `.deb` but lives in this tree (`cmd/hesplay/`,
`build/nfpm-hesplay.yaml`).

### D3 — The contract is Accept-JSON on the existing routes, emitting declared DTOs *(settled 2026-08-15, auto-adopted: house idiom)*
Not a new `/api/v1` namespace. `requestWantsJSON` (`handlers_core.go:229`)
already branches scan/delete/integrity/jobs, and `hescli` consumes exactly that.
Extending it to the browse handlers reuses one query per concept, so "what a
series row is" keeps one owner. The discipline that makes this safe is **L3**:
the JSON branch marshals a named DTO, never the page view model, and a guard
test pins the fields.

### D4 — Capability negotiation, not a static Roku profile *(settled 2026-08-15, auto-adopted)*
Roku models differ enormously in decode support, and a 1080p TV is exactly the
case where a generous static profile would lie. The channel reads
`roDeviceInfo.CanDecodeVideo()` / `CanDecodeAudio()` /
`GetAudioOutputChannel()` at boot and passes a compact capability descriptor to
the session endpoint; the server builds a `ClientProfile` from it, falling back
to a conservative static `roku` row when absent. The static row is the floor,
not the answer.
*Provenance:* [roDeviceInfo](https://developer.roku.com/docs/references/brightscript/components/rodeviceinfo.md).

### D5 — Direct-play-or-nothing is the design constraint *(settled 2026-08-15, Dan: 1080p Roku TV + Pi 5 server)*
See **L1**. Consequence: the codec/container matrix work in P01 is not an
optimization pass, it is the feature. The measured precedent is in this repo —
copying video instead of transcoding it was 74.9 → 2.4 CPU-s per minute of
1080p (31×, `CLAUDE.md` §TV Streaming).

### D6 — Transport by decision: direct play, HLS, and *no* progressive remux *(settled 2026-08-15, auto-adopted, flagged for Stage 3)*
- **Direct play** → `/stream/tv/{id}` etc. `http.ServeContent` gives byte ranges,
  so Roku seeks natively. This is the intended path for most of the library.
- **Transcode** → HLS. Hespera's segment-on-demand VOD playlist is already
  seekable and Roku is an HLS-first platform.
- **Progressive remux is disabled for Roku.** That arm is browser-shaped: a
  chunked fMP4 pipe with no range support, whose scrubber only works because of
  the `moov` duration patch, and which deliberately `panic(http.ErrAbortHandler)`s
  on failure to defeat a Chrome-specific lie. None of that helps a Roku, and an
  unseekable stream on a TV remote is a defect.
- **Open seam for Stage 3:** a file whose codecs are fine but whose container
  Roku won't take (`.avi`, raw `.ts`) currently has no cheap arm — remux is
  disabled and transcode violates L1. The candidate answer is an HLS variant
  that stream-*copies* into segments. Not yet designed.

### D7 — Subtitles are sidecar VTT; burn-in is a last resort *(settled 2026-08-15, auto-adopted)*
Roku takes a subtitle URL in content metadata and supports WebVTT/SRT/TTML, and
Hespera already extracts VTT sidecars (`/stream/tv-subtitles/`, overlap-clamped
by `clampVTTOverlaps`). Bitmap subs (PGS/DVD/DVB) force burn-in, which is a
transcode — under **L1** that is surfaced as a choice, never taken silently.

### D8 — Network posture is unchanged *(settled 2026-08-15, auto-adopted: no new exposure)*
The Roku reaches Hespera over the LAN, which means `HESPERA_LISTEN=:8080` — the
existing, documented operator opt-in. This adds no new exposure: `hesplay`
already talks to a LAN Hespera with no authentication, and the Pi 5 is already
LAN-exposed for it. `csrfGuard` passes requests omitting both `Origin` and
`Referer` (`csrf.go:60`), so a BrightScript `roUrlTransfer` POST is not blocked
and needs no header spoofing. **L2** bounds what the channel may call.
*Residual:* an unauthenticated LAN API is a posture Dan has already accepted
twice; this plan does not re-litigate it, and does not widen it.

### D9 — Server discovery: UDP responder, with manual entry as the floor *(settled 2026-08-15, auto-adopted)*
Text entry on a TV remote is miserable, so the channel broadcasts a discovery
probe (`roDatagramSocket`) and Hespera answers with its base URL and version.
Manual `host:port` entry ships first and always works — AP isolation, VLANs and
guest Wi-Fi all break broadcast, and a client that can only auto-discover is
stranded on those networks. The chosen server persists in the Roku registry.

### D10 — Absolute URLs are composed client-side *(settled 2026-08-15, auto-adopted)*
`streamURL` (`handlers_stream_tv.go:257`) returns relative paths, and every web
client resolves them against its own origin. The Roku knows the base URL it just
queried, so it concatenates. The server contract stays unchanged and stays
correct for both client kinds.

### D11 — Scope: six verticals; Books excluded *(settled 2026-08-15, Dan)*
**In:** TV, Movies, Photos (incl. home-video clips), Music, Audiobooks,
Podcasts. **Out:** Books — EPUB and PDF need a renderer the device does not
have, and shipping a shelf whose items cannot be opened is worse than not
shipping the shelf. (A CBZ-as-images reader is technically viable and is
recorded as declined-for-now, not impossible.)
Also out by **L2**: settings, match review, tag editor, scan/delete, duplicates.

### D12 — Distribution: sideload now, nothing blocking a store path later *(settled 2026-08-15, from the brief)*
Developer Mode sideload via the device's `/plugin_install` endpoint. Dev mode
persists across reboots (verified — the widely-repeated "expires after 72
hours" is wrong); one sideloaded app at a time; the debug console is telnet on
port 8085. **L5** keeps the later options open, and they are not hypothetical:
Jellyfin ships a *public* Roku channel that talks plain HTTP to a LAN server, so
the store path is not foreclosed by this design.

### D13 — Music leveling cannot be reproduced on the device *(open — resolve in Stage 3)*
Hespera's one automatic music behaviour is a client-side `GainNode` applying
per-track `gainDb` (`player.js`), and the EQ is ten biquads in the same graph.
BrightScript exposes no equivalent per-stream gain. Candidate resolutions:
(a) music plays unleveled on the TV and the plan says so; (b) the server applies
gain, which means re-encoding and collides with **L1**; (c) leveling is dropped
for Roku and the loudness data is surfaced some other way. **Not settled** —
this is a real fidelity regression against the web player and it needs the Stage
3 audio round. Verify the BrightScript audio API before choosing.

### D14 — Trickplay sprites do not transfer *(settled 2026-08-15, auto-adopted)*
Hespera's scrubber preview is a JPEG sprite sheet positioned by CSS
`background-position` — a browser mechanism. Roku's trick mode is driven by
HLS/DASH I-frame playlists instead. For P03 the channel ships Roku's native
trick mode on the HLS path and a timestamp-only scrubber on direct play; the
sprite cache is untouched and unused by the TV.

---

## Build order

### P00 — Bootstrap *(N/A 2026-08-15 — repo exists)*
`/createplan`'s Stage 8 scaffolding does not apply: Hespera is a live repo at
v0.45.36 with its own `CLAUDE.md`, `VERSION`, standards kit, build and install
scripts. No `git init`, no template instantiation, no version reset. This entry
exists so the coordinate space is unambiguous for `/createcode`.

### P01 — The client contract (server-side, no Roku code)
Goal: make Hespera fully drivable by a non-browser TV client — JSON browse for
all six verticals, a capability-negotiated Roku profile, and LAN discovery —
verifiable end to end with `curl` alone, before a single line of BrightScript.

Exit criteria:
- Every in-scope vertical can be browsed to a playable id over JSON.
- A `client=roku` session returns direct play for the library's dominant shapes,
  and its decision is reproducible from the stored probe.
- DTO guard test passes; no browse JSON reads a template view model.
- Discovery answers on the LAN and the manual path works without it.

#### P01.S01 — `roku` profile + capability negotiation
Scope: static conservative `roku` row in `internal/playback/profiles.go` plus a
descriptor-driven `ClientProfile` builder. Refs: D4, D5, D6.
Acceptance: table tests over the real decision matrix (h264+ac3 in mkv → direct
play; unknown capability → fails safe toward transcode; absent descriptor →
static floor).

#### P01.S02 — Browse DTOs + the JSON branch discipline
Scope: the declared DTO file, the marshal helper, and the guard test that pins
the field set. Refs: D3, L3.
Acceptance: a field renamed in a template does not change the JSON; the guard
test fails when a DTO field is dropped.

#### P01.S03 — TV browse JSON (series list, series detail, season detail)
Scope: Accept-JSON branches over the canonical `tvSeriesListBase` queries,
including Continue Watching and resume state. Refs: D3.

#### P01.S04 — Movies browse JSON
Scope: the `movieListBase` twin, incl. detail and Continue Watching.

#### P01.S05 — Photos, Music, Audiobooks, Podcasts browse JSON
Scope: the remaining four verticals' list/detail shapes. Refs: D11, D13.

#### P01.S06 — LAN discovery responder
Scope: UDP probe/answer carrying base URL + version, kill-switchable, plus the
`/healthz` identity the client already needs. Refs: D9, L5.

#### P01.S07 — Session hardening for a TV client
Scope: the D6 seam — decide and implement the answer for compatible-codec /
incompatible-container files; confirm subtitle sidecar URLs, `stream_start_seconds`
and skip segments read correctly for a non-browser consumer. Refs: D6, D7.

### P02 — The channel skeleton and the install pipeline
Goal: a Dev tile on the real TV that finds the server and lists the libraries.
Exit criteria: `make roku` produces a correctly-rooted ZIP; the deploy script
fails loudly on the endpoint's 200-with-error-page; the channel connects by
discovery *and* by manual entry; telnet debugging documented in the README.

Slices (sketch): manifest + icons + splash; `main.brs` + `MainScene`; the build
target; the deploy script with gitignored credentials; the connect/settings
screen with registry persistence; the home screen.

### P03 — Video: TV and Movies
Goal: the crown jewel working on the TV — browse to an episode, play it, resume
it, and have the server agree about what happened.
Exit criteria: direct play confirmed on the real library; resume round-trips
through the earn-only progress rule; subtitles, skip-intro and Up Next work;
a forced transcode is visible, not silent.

### P04 — Photos and home-video clips
Goal: the photo grid, the viewer, and clip playback through the same session
layer.

### P05 — Audio: Music, Audiobooks, Podcasts
Goal: the three audio verticals, with D13 resolved and its answer implemented.
Audiobook chapter stepping reuses the server's chapter marks.

### P06 — Parity and polish
Goal: search, Continue Watching across verticals, deep links, honest error
surfaces, and measured performance on the 1080p panel.

---

## Out of scope

- **Books on Roku** (D11) — and with it any EPUB/PDF rendering.
- **Administrative surfaces** (L2) — settings, scanning, matching, tag editing,
  library deletion, power control.
- **Channel Store submission and Beta channel enrolment** (D12) — designed
  around, not built.
- **DRM, multi-user profiles, and per-device watch state** — Hespera has none of
  these today and the TV does not introduce them.
- **Legal, GTM, and marketing work** — out of scope for this arc by rule; later,
  via their own skills.

## Standing caveats and open doubts

1. **D13 (music leveling) is unresolved** and is the one known fidelity
   regression against the web player.
2. **HEVC on the target device is unknown** until the real Roku is queried
   (`http://<ip>:8060/query/device-info`). D4 makes this a runtime answer rather
   than a planning assumption, but it decides how much of the library direct-plays.
3. **DTS is passthrough-only** — it needs a receiver, not TV speakers. The
   capability descriptor must reflect the *sink*, not the device.
4. **AV1 is DASH-only on Roku**; Hespera speaks HLS. Treat AV1 as transcode-bound
   until proven otherwise.
5. **The Pi 5 is the ceiling.** Any design that leans on transcoding is wrong
   here even when it is correct in general (L1).
6. **One sideloaded app at a time** — the TV cannot hold a stable build and a
   test build simultaneously.
