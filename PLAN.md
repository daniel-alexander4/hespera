# PLAN — Hespera on a Roku TV

**Status: planning.** This document is the single source of truth for the Roku
work. Where this plan and the original brief (the hand-off "Get this app running
on a Roku TV") differ, **the plan wins** — the brief's Task 0 asked for an
assessment before any code, and this is that assessment carried through into a
build order.

This plan is scaffolding. It retires into `CLAUDE.md` + ADRs once built
(STANDARDS §15.6); until then, amendments accumulate here.

**Dateline.** Seeded 2026-08-15 from the Roku hand-off brief + a code and
external-reality recon (D1–D14, build order P00–P06); grilled + deepdived 2026-08-15
(now D1–D16, build order P00–P07, P03 being the channel skeleton)
against Roku's own docs and this repo's actual lines — three external claims
cracked (D7, D14, and D6's scope), L1 re-graded on measured Pi 5 numbers, D15
added as a measurement gate, and a `/deepdive` of `playback.Decide` turned the
new streaming arm's blast radius into a nine-item checklist (P02).

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
smaller half, plus a conditional streaming-arm phase (P02) whose necessity one
measurement decides. The channel itself: 4 phases (P04–P07), because every screen is
new code in a language nothing else in the tree uses. Nothing here is
infeasible, and nothing needs a capability Roku lacks — *for the six verticals
in scope*. Books are the exception and are cut (D11).

---

## Repo laws (the invariants this work may not break)

- **L1 — The Roku is the second concurrent stream, by construction.**
  ~~Direct play or say so… the Pi 5 cannot sustain concurrent transcodes.~~
  **(grill pin: re-graded on measured numbers, 2026-08-15)** The original law
  was over-graded. Measured (project memory, `pi-replaces-plex`): the Pi 5 has
  **no hardware encoder at all**; one transcode is fine, **two concurrent HEVC
  transcodes ≈ 90% duty cycle**, three falls behind. The sharp fact is not that
  transcoding is forbidden — it is that the Pi 5 *already drives its own TV
  window*, so a Roku session is the second stream whenever both are watching.
  The law is therefore: **the channel must never silently add a transcode**, and
  the direct-play fraction is a measured exit criterion (D15), not an aspiration.
  Roku's decode set is wider than Chrome's — MKV/MP4/MOV, H.264/HEVC/AV1,
  AAC/MP3/FLAC/ALAC + AC3/E-AC3/DTS passthrough — so exploiting it can *reduce*
  the Pi's transcode load below what its own Chromium window costs today (614
  HEVC files always transcode for that browser and may direct-play on a Roku).
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

### D5 — Maximising direct play is the feature, not an optimization *(settled 2026-08-15, Dan: 1080p Roku TV + Pi 5 server; amended by the grill)*
See **L1**. The codec/container matrix work in P01 is the point of the project.
The measured precedent is in this repo — copying video instead of transcoding it
was 74.9 → 2.4 CPU-s per minute of 1080p (31×, `CLAUDE.md` §TV Streaming).
**(grill pin, 2026-08-15)** Measured against a real library (danlap's dev DB,
136 files, read-only probe): every file is mkv or mp4, 40% is HEVC, 26% carries
AC-3/E-AC-3. Depending on two device bits alone, the direct-play fraction spans
**51%–100%**. That range is the project's economics and D15 collapses it.

### D6 — Transport by decision: direct play, HLS, and *no* progressive remux *(settled 2026-08-15; seam resolved into P02 by the grill)*
- **Direct play** → `/stream/tv/{id}` etc. `http.ServeContent` gives byte ranges,
  so Roku seeks natively. This is the intended path for most of the library.
- **Transcode** → HLS. Hespera's segment-on-demand VOD playlist is already
  seekable and Roku is an HLS-first platform.
- **Progressive remux is disabled for Roku.** That arm is browser-shaped: a
  chunked fMP4 pipe with no range support, whose scrubber only works because of
  the `moov` duration patch, and which deliberately `panic(http.ErrAbortHandler)`s
  on failure to defeat a Chrome-specific lie. None of that helps a Roku, and an
  unseekable stream on a TV remote is a defect.
- ~~**Open seam for Stage 3:** a file whose codecs are fine but whose container
  Roku won't take (`.avi`, raw `.ts`) has no cheap arm. The candidate answer is
  an HLS variant that stream-*copies* into segments. Not yet designed.~~
  **(grill pin: the seam is much larger than "odd containers", 2026-08-15)**
  The population isn't rare containers — it is **any file whose audio the device
  cannot decode**, which is 26% of the measured library. And the arm is
  structurally blocked in a way the first pass missed: **`video.VODPlaylist`
  (`ffmpeg.go:415`) synthesises uniform `EXTINF`s**, which is only valid because
  every segment is independently re-encoded with a forced keyframe at its first
  frame. `-c:v copy` cannot force a keyframe, so segment boundaries must fall on
  the source's own keyframes and the playlist must carry their real durations.
  A copy arm therefore needs either a keyframe index or a one-pass
  ffmpeg-written playlist — it cannot reuse the synthetic one.
  **One consolation, from the muxer:** AC-3/E-AC-3 are excluded from
  `fmp4CopyableAudio` (`playback.go:187`) for a *fragmented-MP4* reason, not a
  client one — and Hespera's HLS segments are already MPEG-TS (`seg%05d.ts`),
  which carries AC-3 natively. So a TS-based copy arm can copy h264+AC-3 with no
  re-encode at all, provided the device decodes it. This is promoted to its own
  conditional phase (**P02**) with the deepdive's checklist as its acceptance
  criteria, and it is a genuine improvement for Chrome too: that same h264+AC-3
  shape takes the unseekable progressive remux today.

### D7 — Subtitles are sidecar **SRT**; burn-in is a last resort *(settled 2026-08-15; corrected by the grill)*
~~Roku supports WebVTT/SRT/TTML and Hespera already extracts VTT sidecars.~~
**(grill correction, 2026-08-15 — this was wrong)** Roku's *sideloaded* caption
formats are **srt, ttml and dfxp — WebVTT is not among them**. Hespera emits
WebVTT everywhere (`extractVTT`, the OpenSubtitles cache, `/stream/tv-subtitles/`),
so the Roku path needs an **SRT sidecar twin** carrying the same overlap clamp
(`clampVTTOverlaps`) — two cues active at once is a defect in either format, and
the clamp is the only thing preventing it. Same ffmpeg call, different `-f`.
Bitmap subs (PGS/DVD/DVB) still force burn-in, which is a transcode — under
**L1** that is surfaced as a choice, never taken silently.

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

### D13 — Music plays unleveled on the TV *(settled 2026-08-15 by the grill — was open)*
Hespera's one automatic music behaviour is a client-side `GainNode` applying
per-track `gainDb` (`player.js`), and the EQ is ten biquads in the same graph.
**Verified:** BrightScript exposes no per-stream gain for playback — volume is
device-level and deliberately not app-controllable; `roAudioResource`'s 0–100
volume is for sound effects, not the `Video`/`Audio` playback path. Of the three
candidates, **(a) unleveled-and-documented wins**: (b) means re-encoding every
music stream to apply a gain, which also destroys byte-range seeking, and (c)
"surface it some other way" is not a fix. The files are untouched either way —
this is a playback-time convenience the device cannot offer.
**This is a real, permanent fidelity regression against the web player**, logged
to `pending.md` because a future Roku OS audio API would change the answer.

### D14 — Trickplay transfers, repackaged as BIF *(settled 2026-08-15; corrected by the grill)*
~~Trickplay sprites do not transfer; Roku's trick mode is driven by HLS/DASH
I-frame playlists, so the TV gets a timestamp-only scrubber on direct play.~~
**(grill correction, 2026-08-15 — this was wrong)** Roku's trick-mode mechanism
is a **BIF file** (Base Index Frames) attached via `Content.SDBifUrl` /
`HDBifUrl` / `FHDBifUrl`. A BIF is a documented, trivial binary container: a
magic number, version, frame count, timestamp multiplier, an index of
(timestamp, offset) pairs, then concatenated **JPEGs** — all little-endian, and
writable from Go with `encoding/binary` alone.

Hespera's `video.GenerateTrickplay` already extracts exactly that: keyframe-only
JPEGs at `fps=1/10`, 240px wide. Roku wants 320px (HD) JPEGs at a fixed
interval. **So the extraction is already done and only the packaging differs** —
a BIF writer over the existing keyframe pass, not a new pipeline. The sprite
sheets stay for the browser; the TV gets BIFs from the same frames.

### D15 — Measure the device before building against it *(settled 2026-08-15 by the grill)*
The whole project's economics turn on **two bits**: does this specific 1080p
Roku decode **HEVC**, and does it decode **E-AC-3**? Across the measured library
those two answers move the direct-play fraction between **51% and 100%** — the
difference between "the Roku unloads the Pi" and "the Roku doubles its transcode
work while the Pi is already driving its own TV" (**L1**).

This is not a design question, it is one HTTP GET
(`http://<roku-ip>:8060/query/device-info`, Roku's keyless ECP endpoint),
confirmed in-channel later by `roDeviceInfo.CanDecodeVideo()`/`CanDecodeAudio()`
(**D4**). It runs as **P01.S00, before any other work**, and its result is
recorded here as a dated pin. A plan whose payoff is unknown by a factor of two,
when the measurement is free, is a plan that has not been costed.

**The census must run against the library that will actually be served** — the
Pi 5's (~3,657 files, including ~185 DivX-era files that the dev set contains
none of), not the dev DB the range above came from. Running it on the wrong host
would retire a seam the real library still populates.

### D16 — DLNA/UPnP considered and declined *(settled 2026-08-15 by the grill)*
Re-opened as the "zero BrightScript" alternative: serve DLNA and let Roku Media
Player — already installed on every Roku — browse it. **Declined on
correctness**: it delivers a folder tree, not Hespera. No posters, no resume, no
Continue Watching, no watched state, no skip-intro — i.e. none of the product.
Recorded here rather than forgotten, because it is the honest **fallback** if
D15's measurement kills the channel: it would still put the library on the TV.

---

## Prerequisites (owner-side, before P01 closes)

1. **Upgrade the Pi 5.** `hespera` (192.168.1.189) is on **0.39.11** — six
   minors behind — and it is the server this targets. Nothing here is testable
   against it until it is upgraded.
2. **Enable Developer Mode on the Roku** and record its IP; the deploy
   credentials go in `.env`, which `.gitignore` already covers (`.gitignore:21`,
   verified — no new ignore rule needed).

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
- The device census (P01.S00) is recorded as a dated pin on **D15**, run against
  the Pi 5's library — the direct-play fraction is a number, not a range.
- Every in-scope vertical can be browsed to a playable id over JSON.
- A `client=roku` session returns direct play for the library's dominant shapes,
  and its decision is reproducible from the stored probe.
- DTO guard test passes; no browse JSON reads a template view model.
- Discovery answers on the LAN and the manual path works without it.

#### P01.S00 — Measure the device and the library *(no product code)*
Scope: query the real Roku (`:8060/query/device-info`), run the read-only
codec census against the Pi 5's DB, and pin both to D15. Refs: D15, L1.
Acceptance: HEVC and E-AC-3 answered yes/no for the actual device; the
direct-play / audio-only-re-encode / full-transcode split stated as three counts
over the real library; **P02's conditionality resolved by those numbers.**

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
Scope: the SRT sidecar twin (D7) and the BIF writer over the existing keyframe
pass (D14); confirm `stream_start_seconds`, skip segments and chapter marks read
correctly for a non-browser consumer. Refs: D7, D14.
*(The D6 seam itself is P02, not here — it is a streaming arm, not session work.)*

### P02 — The copy-video streaming arm *(CONDITIONAL — built only if P01.S00 says it is needed)*
⚠️ **This is not new work — it is an already-deferred item whose gate this
project may trip.** `deferred.md` → *"Keyframe-aligned copy-HLS (make the copy
paths freely seekable)"*, analysed 2026-07-13. Its stated gate is *"the
progressive `?start=`-reload seek becomes a real complaint"*; a Roku that cannot
decode part of the library is a **different** trigger for the same work. **Do
not build it from this plan without checking that item first** — per the
standing lesson that "on the list" ≠ "needed now", the trigger question goes to
Dan before any code.

That entry carries two findings sharper than this plan's own, both of which are
acceptance criteria here:
- **`-ss` + `-c:v copy` preserves pre-roll back to the previous keyframe**, so a
  segment cut at a fixed `index*6` boundary starts *before* its predecessor
  ended → DTS goes backward across the MSE append → precisely the
  `Parsed buffers not in DTS sequence` failure that `-bf 0` and
  `-avoid_negative_ts disabled` already exist to prevent. The current
  per-segment builder cannot be adapted; it must be replaced for this arm.
- **`-vf` + `-c:v copy` is a hard ffmpeg error**, so the 1080p downscale *and*
  the `-fps_mode cfr` that underwrites the uniform manifest both have to go.

Goal: a seekable stream that copies the video and touches only what must be
touched — the arm between "direct play" and "full transcode". Skipped entirely
if the device decodes everything the library holds; that is the point of running
P01.S00 first.

Exit criteria: a copied-video HLS stream plays and seeks on the real device; the
existing transcode path is byte-identical to before; the nine checklist items
below are all discharged.

**Acceptance criteria — the `/deepdive` modification checklist** (subject:
`playback.Decide` and the streaming-arm surface; traced 2026-08-15, every call
site read):
1. Add `RemuxProtocol Protocol` to `ClientProfile` and **set it explicitly on
   all three web profiles** — `Protocol`'s zero value is `""`, not
   `ProtocolFile`. Consult it at `playback.go:161` **and** `:109` (the
   forced-mode arm), which both hardcode `ProtocolFile` today.
2. Make all three URL builders protocol-aware — `streamURL`
   (`handlers_stream_tv.go:257`), `movieStreamURL` (`:163`), `photoStreamURL`
   (`:186`) — all three dispatch on `Decision` alone. **Add tests for the movie
   and photo builders: they have none** (the TV one is pinned at
   `handlers_stream_tv_test.go:540`, so TV fails loudly and the other two fail
   silently).
3. Split `fmp4CopyableAudio` (`playback.go:187`) into a container-aware pair:
   MPEG-TS carries AC-3/E-AC-3, fragmented MP4 cannot. `RemuxAudioNeedsTranscode`
   currently conflates "the client can't decode it" with "the muxer can't carry
   it" and would force AAC for no reason on a TS arm.
4. Add an arm discriminator to `hlsKey` (`ffmpeg.go:620`) or bump
   `segEncodeVersion` — the key is `src|mtime|size|maxHeight|aud|encoder|v5`
   with **no arm field**, and TV, movie and photo all share one cache root
   (`h.tvHLSCacheRoot()`). Copy-segments and transcode-segments for the same
   file would collide, and mixing them at a boundary re-introduces exactly the
   DTS discontinuity `segEncodeVersion` exists to prevent.
5. Replace `VODPlaylist` for this arm with real keyframe-aligned `EXTINF`s.
6. Gate `isRemux` on protocol in all three clones (`tv:250`, `movie:156`,
   `photo:179`) so `StreamStart` is 0 for a segmented arm — a segmented stream
   is not rebased, and reporting a keyframe landing for it shifts every subtitle
   cue on resume, silently.
7. Teach `Profile()` the Roku UA **and** propagate the client hint + capability
   descriptor onto every stream URL. `remuxAudioPlan` re-derives the profile on
   the *stream* request (`handlers_stream_tv.go:368`) and `inferFromUA` matches
   only browser UAs, so a Roku is planned as Chrome. Latent today (no current
   client sends a non-browser UA to these endpoints) — tracked in `pending.md`.
8. Re-verify the audiobook clone stays insulated — it hardcodes
   `protocol := ProtocolFile` (`handlers_audiobooks.go:72`) and collapses any
   non-direct decision (`:78–82`). Keep that deliberately, not accidentally.
9. Update `CLAUDE.md` §TV Streaming: the "DirectStream is always progressive"
   claim and the `fmp4CopyableAudio` note both go stale.

### P03 — The channel skeleton and the install pipeline
Goal: a Dev tile on the real TV that finds the server and lists the libraries.
Exit criteria: `make roku` produces a correctly-rooted ZIP; the deploy script
fails loudly on the endpoint's 200-with-error-page; the channel connects by
discovery *and* by manual entry; telnet debugging documented in the README.

Slices (sketch): manifest + icons + splash; `main.brs` + `MainScene`; the build
target; the deploy script with gitignored credentials; the connect/settings
screen with registry persistence; the home screen.

### P04 — Video: TV and Movies
Goal: the crown jewel working on the TV — browse to an episode, play it, resume
it, and have the server agree about what happened.
Exit criteria: direct play confirmed on the real library; resume round-trips
through the earn-only progress rule; SRT subtitles (D7), skip-intro and Up Next
work; BIF trick mode (D14) on the scrubber; a forced transcode is visible, not
silent.

### P05 — Photos and home-video clips
Goal: the photo grid, the viewer, and clip playback through the same session
layer.

### P06 — Audio: Music, Audiobooks, Podcasts
Goal: the three audio verticals. Music ships unleveled per **D13**, and the
connect screen says so rather than leaving it to be discovered. Audiobook
chapter stepping reuses the server's chapter marks.

### P07 — Parity and polish
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

1. **The one surviving doubt (grill, 2026-08-15):** if the target Roku decodes
   neither HEVC nor E-AC-3, roughly half the library needs full video transcodes
   on a box with no hardware encoder that is already driving its own TV — and no
   amount of client work fixes that. **D15 is the gate that answers it**, and
   **D16 (DLNA) is the fallback** if the answer is bad enough to kill the channel.
2. ~~D13 (music leveling) is unresolved~~ — **settled** as unleveled-and-documented
   (D13); it remains the one permanent fidelity regression against the web player,
   logged to `pending.md`.
3. ~~HEVC on the target device is unknown~~ — **promoted** from a caveat to a
   scheduled measurement (D15 / P01.S00). It is no longer something to remember;
   it is a slice.
4. **DTS is passthrough-only** — it needs a receiver, not TV speakers. The
   capability descriptor must reflect the *sink*, not the device.
5. **AV1 is DASH-only on Roku**; Hespera speaks HLS. Treat AV1 as transcode-bound
   until proven otherwise.
6. **One sideloaded app at a time** — the TV cannot hold a stable build and a
   test build simultaneously.
7. **The measured 51%–100% span came from the dev library**, not the Pi 5's. The
   real library holds ~185 DivX-era files the dev set contains none of, which are
   the population for the container arm. Do not quote the span as a fact about
   the served library — re-measure there (D15).
