# Coding Conventions

**Analysis Date:** 2026-03-05

## Naming Patterns

**Files:**
- Source files use `snake_case.go`: `tags.go`, `tagwrite.go`, `scanner.go`, `handlers_music.go`
- Test files use `snake_case_test.go` co-located with source: `tags_test.go`, `scorer_test.go`
- Handler files are prefixed by domain: `handlers_core.go`, `handlers_music.go`, `handlers_tv.go`, `handlers_match.go`, `handlers_settings.go`
- Single-purpose packages get a single primary file matching the package name: `internal/jobs/jobs.go`, `internal/pathguard/pathguard.go`

**Functions:**
- Exported: `PascalCase` - `ReadTrackMeta`, `ScoreCandidate`, `NormalizeTitle`, `FindDuplicateAlbums`
- Unexported: `camelCase` - `scanNullString`, `parseSlashNumber`, `cleanTitle`, `typeBonus`
- Constructors: `New(deps) *Type` pattern consistently - `New(cfg, db)` in `scan`, `match`, `auth`, `jobs`
- HTTP handlers: unexported methods on `*Handler` matching route purpose - `h.musicHome`, `h.musicAlbumTracks`, `h.authChallenge`
- Test helpers: unexported, prefixed with `open`, `new`, or `insert` - `openTestDB`, `newTestManager`, `insertTestAlbum`

**Variables:**
- Package-level errors: `Err` prefix with `PascalCase` - `ErrJobNotFound`, `ErrQueueFull`, `ErrJobNotCancel`
- Package-level unexported errors: `err` prefix - `errChallengeExpired`, `errInvalidSession`
- Constants: `camelCase` for unexported - `sessionCookieName`, `mbBaseURL`, `apiBase`
- Constants: `PascalCase` for exported - `AudioExtensions`
- Compiled regexps: `re` prefix - `reSXE`, `reXFormat`, `reAnnotation`, `reSeasonDir`, `usernameRe`

**Types:**
- Structs: `PascalCase` - `Config`, `Handler`, `Scanner`, `Matcher`, `TrackMeta`, `TagWriteFields`
- Handler-local row types: `camelCase` with `Row` suffix - `artistRow`, `trackRow`, `albumDetailRow`, `compilationAlbumRow`
- Internal-only types: unexported `camelCase` - `challengeState`, `sessionClaims`, `authConfig`
- Context key types: empty struct - `type ctxKey struct{}`

**Packages:**
- All under `internal/` - nothing is externally importable
- Short, singular lowercase names: `config`, `db`, `auth`, `jobs`, `scan`, `match`, `music`, `web`, `video`, `tmdb`, `tvscan`, `pathguard`

## Code Style

**Formatting:**
- Standard `go fmt` (gofmt) - no custom formatter
- No `.editorconfig`, `.prettierrc`, or custom formatting config files

**Linting:**
- Standard `go vet` - no golangci-lint or other linting tool configured
- Run with: `go vet ./...`

**Line Length:**
- No enforced limit, but lines are generally kept reasonable
- SQL queries use backtick multiline strings for readability

**Braces:**
- Standard Go style (opening brace on same line)

## Import Organization

**Order:**
1. Standard library packages (`context`, `database/sql`, `encoding/json`, `fmt`, `log/slog`, `net/http`, `os`, `path/filepath`, `strings`, `time`)
2. Third-party packages (`github.com/dhowden/tag`, `modernc.org/sqlite`)
3. Internal packages (`isomedia/internal/config`, `isomedia/internal/db`)

**Path Aliases:**
- Package alias used when import path differs from package name: `isodb "isomedia/internal/db"` (used in `internal/match/dedup_test.go`)
- Blank import for driver registration: `_ "modernc.org/sqlite"` (in `internal/db/db.go`)
- No path aliases configured (no `tsconfig.json` equivalent)

**Example:**
```go
import (
    "context"
    "database/sql"
    "fmt"
    "log/slog"
    "strings"
    "time"

    "isomedia/internal/config"
    "isomedia/internal/music"
    "isomedia/internal/pathguard"
    "isomedia/internal/scan"
)
```

## Error Handling

**Patterns:**
- Return `error` as last return value - standard Go convention used everywhere
- Wrap errors with `fmt.Errorf("context: %w", err)` for context propagation:
  ```go
  return fmt.Errorf("query albums: %w", err)
  return fmt.Errorf("open mp3: %w", err)
  return fmt.Errorf("source album not found: %w", err)
  ```
- Use `errors.New()` for static error messages:
  ```go
  return errors.New("ISOMEDIA_LISTEN is required")
  return errors.New("signature is required")
  ```
- Package-level sentinel errors for expected conditions:
  ```go
  var ErrJobNotFound = errors.New("job not found")
  var errInvalidSession = errors.New("invalid session")
  ```
- Check `errors.Is()` for sentinel comparison:
  ```go
  if errors.Is(err, sql.ErrNoRows) { ... }
  if errors.Is(err, context.Canceled) { ... }
  if errors.Is(err, os.ErrNotExist) { ... }
  ```

**Non-fatal errors:**
- Use `slog.Warn` or `slog.Error` and continue (do not return):
  ```go
  slog.Warn("match album failed", "album_id", a.id, "title", a.title, "err", err)
  // Mark as unmatched and continue to next album
  ```
- Discard errors explicitly with `_ =` or `_, _ =`:
  ```go
  _, _ = s.db.Exec(...)  // best-effort DB update
  _ = conn.Close()        // cleanup in defer
  ```

**HTTP error responses:**
- Use `http.Error(w, msg, code)` for plain text errors
- Use `jsonError(w, msg, code)` helper for JSON API responses (defined in `internal/web/handlers_core.go`)
- Return `http.StatusMethodNotAllowed` (405) for wrong HTTP method
- Return `http.NotFound(w, r)` for missing resources
- Return `http.StatusSeeOther` (303) for POST-redirect-GET

**Validation:**
- Validate early, return descriptive errors
- `Config.Validate()` pattern: method on struct returns first validation error
- Input trimming with `strings.TrimSpace()` applied liberally on all user input

## Logging

**Framework:** `log/slog` (structured JSON logging to stdout)

**Setup** (in `cmd/isomedia/main.go`):
```go
slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))
```

**Patterns:**
- `slog.Info` for normal operations: `slog.Info("listening", "addr", cfg.Listen)`
- `slog.Warn` for recoverable issues: `slog.Warn("auth verify denied", "ip", ip, "reason", "rate_limited")`
- `slog.Error` for failures: `slog.Error("template parse failed", "page", p, "err", err)`
- Always pass `"err", err` as the last key-value pair when logging errors
- Use structured key-value pairs, never string formatting: `slog.Info("artist bio saved", "name", a.name)`
- Request logging via middleware in `internal/web/middleware.go`:
  ```go
  slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "duration", time.Since(start).String())
  ```

## Comments

**When to Comment:**
- Package-level doc comments on exported types and functions
- Inline comments for non-obvious logic: `// Single-row DP.`, `// Rate-limit between artists.`
- Section dividers in large handler files: `// --- Music Home ---`, `// --- Album Edit ---`
- `// internal helpers` divider within files

**JSDoc/TSDoc:** Not applicable (Go project)

**Go Doc Style:**
- Short sentence on exported functions: `// Normalize lowercases, strips non-alphanumeric characters...`
- Type comments: `// TagWriteFields contains the fields to write into audio file tags.`
- No function-level docs on unexported functions (convention)

## Function Design

**Size:**
- Utility functions are short (5-30 lines): `Normalize`, `typeBonus`, `yearBonus`, `clientIP`
- Handler functions are medium (30-80 lines): each does query + scan + render
- Pipeline orchestrators are longer but well-sectioned with comments

**Parameters:**
- Use `context.Context` as first parameter for any I/O operation
- Pass `*sql.DB` rather than abstracting behind interfaces (no repository pattern)
- Use struct parameters for groups of related values: `TagWriteFields`, `VerifyInput`
- Handler methods receive `(w http.ResponseWriter, r *http.Request)` - standard http

**Return Values:**
- `(value, error)` for fallible operations
- `(value, score, bool)` for search results with found flag: `BestCandidate`
- Named return values are NOT used

## Module Design

**Exports:**
- Export the minimum needed: constructors, key functions, types used by other packages
- Keep internal helpers unexported
- Dual export pattern when both internal and external use needed:
  ```go
  func IsGenericCompilationArtist(artist string) bool { ... }  // exported
  func isGenericCompilationArtist(artist string) bool { ... }   // unexported wrapper
  ```

**Barrel Files:** Not used. Each package has 1-4 focused `.go` files.

## Dependency Injection

**Pattern:** Constructor injection via `New()` functions, not interfaces.

- `web.Handler` receives `web.Deps{Cfg, DB}` and internally constructs `jobs.Service` and `auth.Manager`
- `scan.Scanner` receives `config.Config` and `*sql.DB` directly
- `match.Matcher` receives `*sql.DB` and `dataDir` string
- `auth.Manager` receives `config.Config` and `*sql.DB`
- Test seams via function fields: `m.verifySSH`, `m.now` on `auth.Manager`

## Database Access

**Pattern:** Raw SQL with `database/sql`, no ORM.

- Use `QueryContext`/`ExecContext` with `r.Context()` for HTTP handlers
- Use `QueryRowContext` for single-row lookups
- Always `defer rows.Close()` after `QueryContext`
- Always check `rows.Err()` after iteration
- Pre-allocate slices with capacity hints: `make([]trackRow, 0, 64)`
- Use `sql.NullString` for nullable columns, convert with `scanNullString()` helper
- Transaction pattern: `BeginTx` + `defer tx.Rollback()` + `tx.Commit()` at end

**SQL formatting:**
- Multiline SQL strings using backtick raw literals
- Indented with tabs within Go files
- `WHERE` clauses aligned

## HTTP Handler Pattern

**Standard handler structure:**
```go
func (h *Handler) handlerName(w http.ResponseWriter, r *http.Request) {
    // 1. Method check
    if r.Method != http.MethodGet {
        w.WriteHeader(http.StatusMethodNotAllowed)
        return
    }
    // 2. Parse/validate input
    // 3. Database queries
    // 4. Render template
    h.render(w, "template.html", map[string]any{...})
}
```

**URL path parameter extraction:**
```go
idStr := strings.TrimPrefix(r.URL.Path, "/music/album/")
idStr = path.Clean("/" + idStr)
idStr = strings.TrimPrefix(idStr, "/")
albumID, err := strconv.ParseInt(idStr, 10, 64)
```

**Template data:** Always `map[string]any{...}` with `"Title"` key included.

**JSON responses:**
```go
w.Header().Set("Content-Type", "application/json")
_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": ...})
```

## Concurrency

**Patterns:**
- `sync.Mutex` for in-memory state protection: `auth.Manager.mu`, `jobs.Service.mu`
- Buffered channels for job queues: `make(chan JobRequest, 128)`
- `context.Context` for cancellation propagation
- `select` with `ctx.Done()` for cancellation checks in loops
- `sync/atomic` for thread-safe counters in tests
- Rate limiters: `time.NewTicker` channel in `tmdb.Client`, manual `time.Since` check in `match.MBClient`

---

*Convention analysis: 2026-03-05*
