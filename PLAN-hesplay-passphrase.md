# PLAN — a three-word passphrase for the hesplay remote

**Status: building.** Single source of truth for this work.

> **Why this is not `PLAN.md`.** `PLAN.md` is the in-flight Roku arc (P00–P07,
> still in planning). One repo, two plans: appending a phase to that document
> would put an unrelated build order inside a plan whose markers another arc
> resumes from. This file carries its own build order and its own markers.

**Dateline.** Seeded 2026-08-22 from a `/grill` of Dan's request — "every
hesplay instance should have a three word passphrase that lets a user in. no
user accounts, just the three word passphrase. without it noone gets in" —
first pass plus adversarial attack, five-seat panel (`bullpen/code-change`) with
the `go/core+service+wire` and `crypto/core` SME packs. Seam inventory at
`<project-memory>/instruments/hesplay-passphrase.md`.

---

## The problem

`hesplay --listen` serves a phone-remote PWA on the LAN with **no
authentication at all** (`cmd/hesplay/control.go:17-21`), and `README.md`
§Security posture documents that as deliberate — so this work edits a stated
posture, not just code. The surface behind it is wider than "playback":

- `PUT /api/noise` rewrites `~/.config/hespera/hesplay.json` wholesale from
  request JSON.
- `PUT /api/server` repoints the upstream Hespera to a caller-supplied URL and
  persists it; `/art/` then proxies `upstream.base + r.URL.Path`.

## Decisions

### D1 — One secret, and the cookie is derived from it *(settled 2026-08-22 by the grill)*
Three words on disk, plaintext, 0600. Cookie value =
`base64url(sha256("hesplay-session-v1\x00" + normalized phrase))`. Buys: survives
restart (so the needrestart every apt deploy triggers does not sign the
household out), a stolen cookie does not yield the spoken words, and changing
the passphrase IS the revocation mechanism. Rejected: a second stored session
token (two secrets to keep in step), and storing a hash of the phrase (breaks
`hesplay passphrase`, whose whole job is answering "what was it again?").

### D2 — EFF long list, 7,772 words, 38.77 bits *(settled 2026-08-22 by the attack — was the 1296-word short list)*
The short list is 31 bits — a ~13-day grind at 1000 req/s — which makes the rate
limiter the load-bearing control. At 38.77 bits the *entropy* is load-bearing
and a limiter bug is an inconvenience. Four hyphenated entries removed because
the normalizer would split them; see `cmd/hesplay/wordlist.go`'s header.

### D3 — `SameSite=Lax`, not `Strict` *(settled 2026-08-22 by the attack)*
Hespera's home Noise card is a plain cross-origin link
(`noise_remote_url`, `internal/web/handlers_settings.go:394`). `Strict` withholds
the cookie on top-level cross-site navigation, so **every arrival from Hespera
would render a login screen despite a valid session.** `Lax` sends it on GET
navigation and still withholds it from cross-site POSTs — which is every
mutating call.

### D4 — Per-IP rate limit, never global *(settled 2026-08-22 by the attack)*
A single global deadline is a remote lockout: one looping attacker keeps it in
the future forever and Dan is refused while holding the right passphrase. An
attacker with many source addresses gets more attempts; at 38.77 bits that does
not matter and not locking the owner out does.

### D5 — A persist failure must not take the noise schedule down *(settled 2026-08-22)*
The reconciler lives inside the `--listen` process. An unwritable config
directory yields an in-memory passphrase, a loud warning, and a serving remote —
never a refusal to start.

### D7 — A non-canonical request path is refused at the door *(settled 2026-08-22 by the S02 deepdive)*
`requireAuth` returns 400 for any path that is not already in cleaned form —
checked on **both** `r.URL.Path` and `r.URL.EscapedPath()`, before it decides
anything else. Both halves earn their place: the decoded check catches
`%2e%2e`, the escaped one catches double-encoding (`%252e%252e`), which survives
a single decode as literal text. Not defence in depth — **load-bearing**: the middleware
decides on a cleaned path while `ServeMux` routes on the raw one, and any
divergence between those two is a bypass. Refusing non-canonical paths makes the
two decisions provably the same input. It also kills the `/art/%2e%2e/..`
traversal into the upstream proxy for free. No legitimate client sends a dot
segment; verified that `/`, `/app.js`, `/api/state` and `/art/album/1` are all
already canonical.

### D6 — Cookies are not port-scoped, and that is accepted *(settled 2026-08-22)*
The session cookie is offered to any other HTTP service on the same hostname.
A property of HTTP cookies, not of this design; the proportionate fix (bearer
token + HMAC-signed art URLs, because `<img>` cannot carry a header) costs more
than the exposure on a single-purpose box. Being host-keyed is also what makes
DNS rebinding a non-event: a rebound origin sends no cookie and gets 401.

## Build order

### P01 — The passphrase gate

**Exit criteria.** Every gated route refuses an unauthenticated request **and**
the static shell still serves; a correct passphrase in any casing or spacing
signs in **and** a wrong one does not **and** an over-budget attempt is refused
without being compared; the passphrase file is 0600 after create **and** after
rotate; a rotate applies without a restart **and** a cookie survives one;
scheduled noise starts at its window with nobody signed in; `go test ./...` and
`go vet ./...` green **and** `go.mod` unchanged.

#### P01.S01 — Wordlist and auth core *(done 2026-08-22, v0.45.39)*
Pure module, nothing wired. `cmd/hesplay/wordlist.go`, `cmd/hesplay/auth.go`,
`cmd/hesplay/auth_test.go`, CC BY attribution.
- T01 — generate `wordlist.go` from the EFF long list, minus hyphenated entries.
- T02 — `auth.go`: normalize, derive, generate, load/persist 0600, ≤1 Hz reload, per-IP bucket, counters.
- T03 — `auth_test.go`: wordlist invariants, normalization table, rotate-without-restart, delete-is-not-a-way-in, limiter charges before comparing, per-IP independence, bounded map, race.
- T04 — `THIRD_PARTY_LICENSES.md` + `third_party/eff-wordlist/NOTICE` + ship it in `build/nfpm-hesplay.yaml`.
- T05 — (added mid-slice) `atomicfile.go`: one atomic write for the package;
  `saveNoiseConfig` stopped hand-rolling its own copy rather than let the file
  carry three.

**Four review rounds.** Round 1 found the cookie was a bare SHA-256 — reversible
offline in ~22s on one GPU, making a security claim in my own header comment
false. Round 2 found the round-1 fix had introduced a **fail-open**: an auth with
an empty passphrase admitted `""`, `"!!!!"`, `"12345"` and `"   "`, because
normalising both sides produced the same MAC. Round 3 found the round-2 fix still
reached the panicking constructor on the very error it was written to handle.
Round 4 found nothing above LOW. Every fix is mutation-verified — the pin goes
red when the bug is put back.

#### P01.S02 — The gate *(done 2026-08-22, v0.45.40)*

**Deepdive fired** (2026-08-22) — the slice modifies `routes()`, `secureControl`,
`handleArt` and `handleState`, none of which this plan authored. It found two
things the plan did not know, both before a line was written:

1. **A middleware wrapping `ServeMux` sees the RAW, uncleaned path**, and
   ServeMux does not clean an *escaped* dot segment. So
   `/art/%2e%2e/%2e%2e/healthz` reaches `handleArt` intact and is concatenated
   onto the upstream base.
2. Consequently the first gating shape — "exempt a path that resolves to a
   static file" — was **bypassable**: `/art/%2e%2e/app.js` cleans to `/app.js`
   (exempt) while the mux routes it to the gated `/art/` handler. Demonstrated
   with a probe, not reasoned.

**→ D7 (below) is the amendment this forced.** Tasks:

- T01 — `requireAuth`: **refuse any non-canonical path with 400 first**, then
  exempt `/api/login` and static files, then require the cookie. Nested
  `secureControl(requireAuth(...))` so a 401 still carries the security headers.
- T02 — `POST /api/login`: POST only, body bounded 4 KiB, source IP via
  `net.SplitHostPort`, `a.login` → cookie `hesplay_session`, `Path=/`,
  `HttpOnly`, `SameSite=Lax`, no `Secure`, 1yr.
- T03 — `serveControl`: build the auth, warn on a degraded start, announce the
  passphrase **only when stdout is a TTY** so it never reaches the journal.
- T04 — `/art/` hardening: whitelist `^/art/album/[0-9]+$`, refuse redirects,
  serve only upstream 200, bound the body, `Cache-Control: private`.
- T05 — `handleState` publishes `authCounts` — the reader those counters have
  been waiting for since S01.
- T06 — tests: in-memory auth helper, cookie helper for the 25 gated requests,
  `TestProductionRoutesAreGated`, and traversal pins.
- Use `a.usable()` to tell "no credential could be established, nobody can sign
  in" from "signed in fine, the key could not be persisted" — `newAuth` returns a
  non-nil auth and a non-nil error in both cases.
- ⚠️ **Test helpers must use `newMemoryAuth`, never `newAuth`.**
  `newTestController()` (`control_test.go:54`) does not set `XDG_CONFIG_HOME` and
  the package has no `TestMain`, so a helper reaching the file-backed
  constructor would write a passphrase into Dan's real `~/.config/hespera` on
  every `go test`. Verified not to happen today; it becomes reachable the moment
  the gate is wired into `routes()`.
- ⚠️ **Auth must run before routing**, so an unauthenticated request gets 401
  rather than 405 — `mux.HandleFunc` registers no method patterns, the method
  checks live inside the handlers.
- ⚠️ `http.MaxBytesReader` on the login body. `auth.login` caps the submitted
  passphrase at `maxPassphraseInput` itself, but the body is the caller's bound.
- Baseline recorded live 2026-08-22: `GET /api/state` answers **200 with no
  credential**. S02's live check is that same request returning **401**.

#### P01.S03 — The client
- ⚠️ **`api()` must carry the status onto the thrown Error** (`app.js:19-27`
  currently throws `new Error(body.message)` and drops `res.status`). Without it
  the login UI cannot tell 401 "sign in" from 503 "this box has no passphrase"
  from a 502 upstream failure — three states that need three different screens.
Login overlay outside the `SCREENS` stack; 401 branch in `api()`; pause the 2s
poll while signed out; `#p=` fragment sign-in + `history.replaceState`; art
`onerror`.

#### P01.S04 — CLI verbs and docs
`hesplay passphrase [set|rotate]` (never in argv); `README.md` §Security posture
and §Remote speakers; `cmd/hesplay/CLAUDE.md`; `usage()`;
`build/hesplay@.service` header.

## Standing caveats

- **Cleartext on the LAN.** Plain HTTP, so the passphrase and the cookie cross
  the wire readable by anything on the wire. "Without it no-one gets in" is true
  against a houseguest, false against a sniffer. README must say so.
- **Hespera itself stays unauthenticated** on the same LAN. This gates the
  smaller blast radius of the two. Named, not resolved — Dan asked for hesplay.
- **`cmd/hesplay/web/app.js` has no automated coverage.** `test/js/harness.js`
  pins `web/static`. S03's behaviour is live-browser-only.
