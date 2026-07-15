package config

// Bundled provider keys — set at link time by build.sh, empty in every
// from-source build.
//
// A fresh Hespera download would otherwise be dead on arrival for its two
// biggest verticals: TV and Movies don't match anything without a TMDB key.
// Rather than make every downloader register their own, the official release
// binaries carry a bundled key per provider as the final fallback tier of the
// effective*Key cascade (app_settings → env → bundled). A user-entered or env
// key still wins, so power users override for higher limits or if a bundled key
// is ever revoked.
//
// These are NOT constants and NOT committed: build.sh injects them with
// `-ldflags -X` from an out-of-repo keys file, exactly like main.version. The
// git tree never holds a key, so a scraped source checkout yields nothing; only
// Dan's release builds carry them. A from-source build or a fork gets empty
// strings here and degrades to user-supplied keys — correct, since it isn't a
// Hespera release and must not spend Hespera's quota or borrow its identity.
//
// Only three providers are bundled, by design:
//   - TMDB — the one hard dependency (TV/Movie matching); embedding is
//     officially the developer's call and is the OSS norm (Jellyfin, Kodi).
//   - fanart.tv — its "project key" is *designed* to be embedded and shared by
//     all installs; the per-user fanarttv_api_key setting stays as the optional
//     personal-key upgrade.
//   - OpenSubtitles — embedding one consumer key is *mandatory*: their terms
//     ban users who register their own key, and the key carries no quota (that
//     rides the end user's account), so a scraped one is near-worthless.
//
// Deliberately NOT bundled:
//   - Last.fm — hostile terms (non-commercial only, no sub-licensing, they
//     reserve the right to cap users-per-key) for a marginal feature (a
//     shuffle-popularity blend). Navidrome shipped a shared key and was forced
//     to remove it over rate limits. Stays user-supplied.
//   - TheAudioDB — legitimate redistribution requires a paid ($8/mo Patreon)
//     key; the free key is dev-only. Not worth the spend for a feature already
//     partly covered by keyless Wikipedia/Wikimedia. Stays user-supplied.
var (
	EmbeddedTMDBKey          string
	EmbeddedFanartKey        string
	EmbeddedOpenSubtitlesKey string
)
