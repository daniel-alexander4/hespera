# PLAN — a three-word passphrase for the hesplay remote

**Status: shipped (v0.45.39–.43).** Single source of truth for this work.

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
`base64url(HMAC-SHA256(instance key, "hesplay-session-v1\x00" + normalized phrase))`,
where the per-instance key is 32 random bytes stored beside the words.

**⚠️ Corrected 2026-08-22 — this decision as first written documented the bug.**
It said `sha256(domain + phrase)` with no key, which the S01 review found is
reversible offline in ~22s on one GPU against a 38.77-bit secret: a stolen
cookie would have handed back the words the household says out loud. The code
was fixed in the same slice and the decision was not, so for four commits the
plan's headline decision described the defect rather than the fix. The key is
also what makes `logout` possible — a new key with the SAME words signs every
phone out.

Buys: survives
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

### D8 — The `#p=<phrase>` URL sign-in is CUT *(settled 2026-08-22 by the S03 attack — it had been a default-to-confirm)*
The original grill proposed letting `noise_remote_url` carry the passphrase in a
fragment, so Hespera's home Noise card would work on the TV — whose app-mode
Chromium, driven by a Flirc remote, has **no keyboard at all**. Cut, because the
cost was mis-weighed:

- `noise_remote_url` is an `app_settings` value rendered as an **`href` on an
  unauthenticated Hespera**. Putting the passphrase in it hands the credential to
  anyone who can load the home page — precisely the population this whole arc
  exists to keep off the speakers. `history.replaceState` cleans the address bar,
  not Hespera's DOM.
- The premise that the TV would need it repeatedly is **false**. The session
  cookie is HttpOnly with a one-year life, and the kiosk Chromium runs a
  persistent dedicated profile — so the TV signs in **once, ever**. One borrowed
  USB keyboard, one time, beats a permanent plaintext credential.

Also corrected: the worry that Add-to-Home-Screen would bake the fragment into
`start_url` does not hold — `start_url: "."` resolves against the manifest and
drops the fragment.

If this is ever revisited, the shape is a single-use short-TTL **enrollment
token**, never the passphrase itself.

### D7 — A non-canonical request path is refused at the door *(settled 2026-08-22 by the S02 deepdive)*
`requireAuth` returns 400 for any path that is not already in cleaned form —
checked on **both** `r.URL.Path` and `r.URL.EscapedPath()`, before it decides
anything else. Both halves earn their place: the decoded check catches
`%2e%2e`, the escaped one catches double-encoding (`%252e%252e`), which survives
a single decode as literal text. No legitimate client sends a dot segment;
verified that `/`, `/app.js`, `/api/state` and `/art/album/1` are all already
canonical.

**⚠️ Corrected 2026-08-22 by the S02 security review — this decision was first
written overclaiming itself.** It said the check was "not defence in depth —
load-bearing". That is **false as implemented**, and the correction was verified
by mutation: with `canonicalPath` removed, every traversal returns 401, not a
bypass, because `isPublicAsset` hands the *uncleaned* name to `fs.ValidPath`,
which rejects a `..` element on its own.

What was true: the bypass is real for the shape this plan **first described** —
clean the path, then look it up in a set of public paths — which is exactly what
the probe that justified D7 modelled. What is true now: the check makes the
property **local and provable here** rather than resting on a downstream
helper's incidental behaviour, and it kills the `/art/%2e%2e/..` traversal into
the upstream proxy on the way past. Keep it; do not oversell it. The general
hazard it guards — a gate whose exemption is computed on `r.URL.Path` while
routing happens on `r.URL.EscapedPath()` — is real regardless of which mechanism
happens to catch it today.

### D6 — Cookies are not port-scoped, and that is accepted *(settled 2026-08-22)*
The session cookie is offered to any other HTTP service on the same hostname.
A property of HTTP cookies, not of this design; the proportionate fix (bearer
token + HMAC-signed art URLs, because `<img>` cannot carry a header) costs more
than the exposure on a single-purpose box. Being host-keyed is also what makes
DNS rebinding a non-event: a rebound origin sends no cookie and gets 401.

## Build order

### P01 — The passphrase gate *(done 2026-08-22, v0.45.44)*

**Exit criteria, credited clause by clause** (split on every `and`; a clause that
cannot be demonstrated is `not exercised`, never a silent pass):

| # | Clause | Verdict |
|---|---|---|
| C1 | Every gated route refuses an unauthenticated request | met — 16 routes × 4 methods + wrong-cookie; live 401 (baseline 200) |
| C2 | the static shell still serves | met — test + live 200 |
| C3 | a correct passphrase in any casing or spacing signs in | met — table + real browser (`Canopy-Spoilage-Chip␣␣`) |
| C4 | a wrong one does not | met — test, live 401, browser inline message |
| C5 | an over-budget attempt is refused **without being compared** | met — the *correct* passphrase 429s and the failure counter does not move; mutation-verified |
| C6 | the passphrase file is 0600 after create | met — test + live `stat` |
| C7 | …and after rotate | met — test + live `stat` |
| C8 | a rotate applies without a restart | met — injected-clock test + CLI against a running remote, <1.5s |
| C9 | a cookie survives one | met — token identical across reconstruction; live restart → old cookie 200 |
| C10 | scheduled noise starts at its window with nobody signed in | **not exercised live** — see below |
| C11 | `go test ./...` green | met |
| C12 | `go vet ./...` green | met |
| C13 | `go.mod` unchanged | met — `git diff eab1394~1 -- go.mod go.sum` empty |

**C10 is the one gap and is recorded as a gap.** Demonstrating it means making
sound, which [[no-playback]] forbids on every box here. What stands instead: the
reconciler goroutine starts at `control.go:515`, **two lines before the auth
exists** (`:517`), and neither it nor a reconcile pass references auth in any
form — both pinned by `TestScheduleIsIndependentOfAuth` and mutation-verified
(moving `openAuth()` ahead of the watcher turns it red). That is a *guard*, not
a demonstration, and the difference is stated rather than blurred.

**Phase-close review found three cross-slice defects no single-slice review
could see**, all fixed in v0.45.44: persistence was inferred from an overloaded
`warning` value (so a garbled-but-successful start advertised itself as
ephemeral and steered the operator away from the file holding the words); the
CLI promised a live reload the remote could not honour on a path-less auth; and
`authCounts`' documented tripwire was unobservable through its only publisher,
because `/api/state` is itself gated.



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

#### P01.S03 — The client *(done 2026-08-22, v0.45.42)*

**Deepdive fired** (2026-08-22) — the slice modifies `app.js`, `index.html` and
`app.css`, none of which this plan authored. Three facts it established that the
plan did not know:

- `app.css:71` is `[hidden] { display: none !important; }`, so a plain `hidden`
  attribute reliably beats any `display` the overlay carries.
- `toast(msg, isErr)` (`app.js:49-57`) is the app's **only** error surface and
  every one of eight error paths already funnels there — so a sign-in failure
  reuses it rather than inventing a second.
- `app.js` reads `location` / `history` / `hash` **nowhere at all**, so a `#p=`
  fragment cannot collide with anything. The screen stack is purely in-memory.

Tasks:
- T01 — `api()` attaches `e.status`/`e.body` (it currently drops `res.status`,
  so 401 *sign in*, 503 *this box has no passphrase* and a 502 upstream failure
  are indistinguishable) and routes 401/503 to the overlay.
- T02 — the overlay: markup after `#toast`, `position:fixed; inset:0; z-index:5`
  (above `.bar` 2, `.now` 3, `.toast` 4), styled off `.panel`/`.btn`.
- T03 — one field, `autocapitalize` off, not `type="password"`; Enter submits via
  a keydown handler rather than a `<form>`, which sidesteps `form-action 'none'`.
- T04 — suspend the poll while signed out (boot must capture the interval handle,
  which it currently discards) and clear the stale paint.
- T05 — **CUT.** See D8.
- T06 — `onerror` on the two album-art `<img>`s, which now 401 while signed out.
- T07 — (added by the attack) extend `test/js/harness.js` to take a static dir
  and add `hesplay_app.test.js`. This slice puts a state machine into a
  1,254-line file with **zero** automated coverage; the harness change is one
  constant becoming a parameter.
- ⚠️ **`api()` must carry the status onto the thrown Error** (`app.js:19-27`
  currently throws `new Error(body.message)` and drops `res.status`). Without it
  the login UI cannot tell 401 "sign in" from 503 "this box has no passphrase"
  from a 502 upstream failure — three states that need three different screens.
Login overlay outside the `SCREENS` stack; 401 branch in `api()`; pause the 2s
poll while signed out; art `onerror`. (The `#p=` fragment sign-in that stood
here was **cut** — see D8.)

#### P01.S04 — CLI verbs and docs *(done 2026-08-22, v0.45.43)*

**Deepdive did NOT fire**, and recording that rather than skipping silently: the
slice adds a case to a verb switch and an entry to a completion list. Additive,
with no record payload, wire format, on-disk layout or multi-implementor seam —
the file format is S01's and `writeAuthFile` already exists. The precedent
(`cmdServer`, `main.go:~225`, dispatched pre-probe at `main.go:64`) was read
directly instead.

**Scope moved.** `README.md`, `usage()`, `build/hesplay@.service` and
`control.go`'s package comment were already corrected in **S02** — they became
*false* the moment the gate landed, and a commit does not get to ship a claim it
has just falsified. What is left here:

- T01 — `hesplay passphrase` — print the words. **Always**, never TTY-gated:
  `announcePassphrase`'s gate protects a long-running service's *journal*, and
  an explicit one-shot request is not that. TTY-gating it would break
  `P=$(hesplay passphrase)` by silently returning a path where a caller expects
  a passphrase.
- T01b — `hesplay passphrase path` — the file, for scripting.
- T02 — `hesplay passphrase set` — read the words from **stdin**, never argv
  (`ps` is world-readable and shell history keeps it). Empty input, EOF-first
  and input that normalises to nothing are **hard errors that write nothing** —
  otherwise the file loses its phrase line and the next start silently mints
  random words, locking the household out.
- T03 — `hesplay passphrase rotate` — generate a fresh one.
- T03b — `hesplay passphrase logout` — new instance key, **same words**. The
  passphrase file's own comment advertises exactly this operation ("delete it to
  sign every phone out without changing the words") and the CLI would otherwise
  omit the thing its own file documents.
- T03c — **refuse under sudo.** `os.Geteuid()==0 && $SUDO_USER != ""` names the
  resolved path and `sudo -u <user> …` instead. Root writing into dan's 0700
  directory *succeeds* — producing a root-owned 0600 file the remote (as `dan`)
  cannot read, which degrades to an ephemeral passphrase that changes on every
  restart and is unrecoverable without sudo. And every write re-reads the file
  afterwards and prints what is actually on disk.
- T03d — **`passphrase` and `path` must use `readAuthFile`, never `newAuth`** —
  `newAuth` *generates and persists* on a fresh box and *renames* a garbled file
  aside. A read-only verb must not mint a credential.
- T04 — dispatch pre-probe beside `server`: this is pure file work and has to
  run when no Hespera is up.
- T05 — `passphrase` into the completion verb list **and its own sub-arg case**
  beside `server)`'s — the bare verb list alone drops it into `*) COMPREPLY=()`.
- T06 — tests over an isolated `XDG_CONFIG_HOME`.
- T07 — `cmd/hesplay/CLAUDE.md`. ⚠️ **Gitignored** (`**/CLAUDE.md`, and no
  CLAUDE.md is tracked), so this edit is local-only and never rides in the
  commit — worth knowing before `/tidy` rather than discovering inside it.

## Standing caveats

- **Cleartext on the LAN.** Plain HTTP, so the passphrase and the cookie cross
  the wire readable by anything on the wire. "Without it no-one gets in" is true
  against a houseguest, false against a sniffer. README must say so.
- **Hespera itself stays unauthenticated** on the same LAN. This gates the
  smaller blast radius of the two. Named, not resolved — Dan asked for hesplay.
- ~~`cmd/hesplay/web/app.js` has no automated coverage.~~ **Closed by S03 T07**:
  `test/js/harness.js` takes a directory now and `test/js/hesplay_app.test.js`
  covers the sign-in state machine. What remains live-browser-only is the part
  jsdom cannot model — a phone keyboard's autocapitalize, cookie survival across
  a PWA relaunch, and the TV's arrow-key focus ring.
