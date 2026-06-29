package db

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

const schemaSQL = `
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS libraries (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL CHECK(type IN ('music','movies','tv','photos','home_videos')),
  root_path TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS music_artists (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  art_path TEXT NOT NULL DEFAULT '',
  musicbrainz_id TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '',
  bio_source_name TEXT NOT NULL DEFAULT '',
  bio_source_url TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(library_id, name)
);

CREATE TABLE IF NOT EXISTS music_albums (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  artist_id INTEGER NOT NULL REFERENCES music_artists(id) ON DELETE CASCADE,
  album_artist_id INTEGER NOT NULL DEFAULT 0,
  is_compilation INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL,
  year INTEGER NOT NULL DEFAULT 0,
  art_path TEXT NOT NULL DEFAULT '',
  musicbrainz_id TEXT NOT NULL DEFAULT '',
  artist_musicbrainz_id TEXT NOT NULL DEFAULT '',
  match_status TEXT NOT NULL DEFAULT '',
  match_confidence REAL NOT NULL DEFAULT 0.0,
  match_source TEXT NOT NULL DEFAULT '',
  matched_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(library_id, artist_id, title, year)
);

CREATE TABLE IF NOT EXISTS music_tracks (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  artist_id INTEGER NOT NULL REFERENCES music_artists(id) ON DELETE CASCADE,
  album_id INTEGER NOT NULL REFERENCES music_albums(id) ON DELETE CASCADE,
  title TEXT NOT NULL,
  track_no INTEGER NOT NULL DEFAULT 0,
  disc_no INTEGER NOT NULL DEFAULT 0,
  abs_path TEXT NOT NULL,
  mime_type TEXT NOT NULL DEFAULT 'application/octet-stream',
  file_size_bytes INTEGER NOT NULL DEFAULT 0,
  mtime_unix INTEGER NOT NULL DEFAULT 0,
  checksum_sha256 TEXT NOT NULL DEFAULT '',
  popularity INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(library_id, abs_path)
);

CREATE INDEX IF NOT EXISTS idx_music_albums_artist_id ON music_albums(artist_id);
CREATE INDEX IF NOT EXISTS idx_music_albums_album_artist_id ON music_albums(album_artist_id);
CREATE INDEX IF NOT EXISTS idx_music_albums_match_status ON music_albums(match_status);
CREATE INDEX IF NOT EXISTS idx_music_tracks_album_id ON music_tracks(album_id);
CREATE INDEX IF NOT EXISTS idx_music_tracks_artist_id ON music_tracks(artist_id);
CREATE INDEX IF NOT EXISTS idx_music_tracks_size_checksum ON music_tracks(library_id, file_size_bytes, checksum_sha256);

CREATE TABLE IF NOT EXISTS play_history (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  track_id INTEGER NOT NULL REFERENCES music_tracks(id) ON DELETE CASCADE,
  library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  artist_id INTEGER NOT NULL REFERENCES music_artists(id) ON DELETE CASCADE,
  album_id INTEGER NOT NULL REFERENCES music_albums(id) ON DELETE CASCADE,
  played_ms INTEGER NOT NULL DEFAULT 0,
  completed INTEGER NOT NULL DEFAULT 0,
  source TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_play_history_track_id ON play_history(track_id);
CREATE INDEX IF NOT EXISTS idx_play_history_album_id ON play_history(album_id);
CREATE INDEX IF NOT EXISTS idx_play_history_artist_id ON play_history(artist_id);
CREATE INDEX IF NOT EXISTS idx_play_history_created_at ON play_history(created_at DESC);

CREATE TABLE IF NOT EXISTS tv_series_files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  abs_path TEXT NOT NULL,
  container TEXT NOT NULL DEFAULT '',
  file_size_bytes INTEGER NOT NULL DEFAULT 0,
  mtime_unix INTEGER NOT NULL DEFAULT 0,
  stream_info_json TEXT NOT NULL DEFAULT '{}',
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(library_id, abs_path)
);

CREATE INDEX IF NOT EXISTS idx_tv_series_files_library_id ON tv_series_files(library_id);

CREATE TABLE IF NOT EXISTS tv_series_identities (
  file_id INTEGER PRIMARY KEY REFERENCES tv_series_files(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'unmatched' CHECK(status IN ('matched','unmatched','skipped')),
  provider TEXT NOT NULL DEFAULT '',
  series_id TEXT NOT NULL DEFAULT '',
  season_number INTEGER NOT NULL DEFAULT -1,
  episode_numbers_csv TEXT NOT NULL DEFAULT '',
  match_confidence REAL NOT NULL DEFAULT 0.0,
  match_method TEXT NOT NULL DEFAULT '',
  matched_at TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_tv_series_identities_provider_series ON tv_series_identities(provider, series_id);
CREATE INDEX IF NOT EXISTS idx_tv_series_identities_status ON tv_series_identities(status);
-- Per-series/season pages filter series_id WITHOUT provider, so the (provider,
-- series_id) composite above can't serve them; this index covers the
-- series_id+status+season lookups that scale with title count.
CREATE INDEX IF NOT EXISTS idx_tv_series_identities_series_id ON tv_series_identities(series_id, status, season_number);

CREATE TABLE IF NOT EXISTS tv_series_metadata_cache (
  entity_key TEXT NOT NULL,
  lang TEXT NOT NULL DEFAULT 'en',
  payload_json TEXT NOT NULL DEFAULT '{}',
  fetched_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY(entity_key, lang)
);

CREATE TABLE IF NOT EXISTS tv_series_art (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  art_type TEXT NOT NULL CHECK(art_type IN ('series_poster','series_backdrop','season_poster','episode_still')),
  tmdb_series_id INTEGER NOT NULL DEFAULT 0,
  season_number INTEGER NOT NULL DEFAULT -1,
  episode_number INTEGER NOT NULL DEFAULT -1,
  art_path TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(art_type, tmdb_series_id, season_number, episode_number)
);

-- Cast/crew people (global TMDB entities, not library-scoped) and the credits
-- join that links a person to a title. media_type discriminates tv vs movie so
-- the movie scanner can reuse this set later. Profile image cached to disk like
-- other art (thumbgc TV sweep references people.art_path).
CREATE TABLE IF NOT EXISTS people (
  tmdb_id INTEGER PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  profile_path TEXT NOT NULL DEFAULT '',
  art_path TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '',
  bio_fetched_at TEXT NOT NULL DEFAULT '',
  filmography_json TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS credits (
  person_id INTEGER NOT NULL,
  media_type TEXT NOT NULL DEFAULT 'tv' CHECK(media_type IN ('tv','movie')),
  media_id INTEGER NOT NULL,
  character_name TEXT NOT NULL DEFAULT '',
  billing_order INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY(person_id, media_type, media_id)
);

CREATE INDEX IF NOT EXISTS idx_credits_media ON credits(media_type, media_id, billing_order);
CREATE INDEX IF NOT EXISTS idx_credits_person ON credits(person_id);

CREATE TABLE IF NOT EXISTS tv_playback_progress (
  file_id INTEGER PRIMARY KEY REFERENCES tv_series_files(id) ON DELETE CASCADE,
  position_seconds REAL NOT NULL DEFAULT 0,
  duration_seconds REAL NOT NULL DEFAULT 0,
  completed INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS movie_files (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  abs_path TEXT NOT NULL,
  container TEXT NOT NULL DEFAULT '',
  file_size_bytes INTEGER NOT NULL DEFAULT 0,
  mtime_unix INTEGER NOT NULL DEFAULT 0,
  stream_info_json TEXT NOT NULL DEFAULT '{}',
  guessed_title TEXT NOT NULL DEFAULT '',
  year INTEGER NOT NULL DEFAULT 0,
  tmdb_id INTEGER NOT NULL DEFAULT 0,
  match_status TEXT NOT NULL DEFAULT '',
  match_confidence REAL NOT NULL DEFAULT 0.0,
  match_source TEXT NOT NULL DEFAULT '',
  matched_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(library_id, abs_path)
);

CREATE INDEX IF NOT EXISTS idx_movie_files_library_id ON movie_files(library_id);
CREATE INDEX IF NOT EXISTS idx_movie_files_match_status ON movie_files(match_status);

CREATE TABLE IF NOT EXISTS movie_metadata_cache (
  entity_key TEXT PRIMARY KEY,
  lang TEXT NOT NULL DEFAULT 'en',
  payload_json TEXT NOT NULL DEFAULT '{}',
  fetched_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS movie_art (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  tmdb_movie_id INTEGER NOT NULL DEFAULT 0,
  art_type TEXT NOT NULL DEFAULT '',
  art_path TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_movie_art_tmdb_id ON movie_art(tmdb_movie_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_movie_art_unique ON movie_art(tmdb_movie_id, art_type);

CREATE TABLE IF NOT EXISTS movie_playback_progress (
  file_id INTEGER PRIMARY KEY REFERENCES movie_files(id) ON DELETE CASCADE,
  position_seconds REAL NOT NULL DEFAULT 0,
  duration_seconds REAL NOT NULL DEFAULT 0,
  completed INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS scan_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id INTEGER NOT NULL DEFAULT 0,
  job_type TEXT NOT NULL DEFAULT 'scan',
  status TEXT NOT NULL DEFAULT 'queued',
  progress_current INTEGER NOT NULL DEFAULT 0,
  progress_total INTEGER NOT NULL DEFAULT 0,
  payload_json TEXT NOT NULL DEFAULT '',
  created_by TEXT NOT NULL DEFAULT 'system',
  duration_ms INTEGER NOT NULL DEFAULT 0,
  cancel_requested INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  error TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL DEFAULT '',
  ended_at TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_scan_jobs_status ON scan_jobs(status);
CREATE INDEX IF NOT EXISTS idx_scan_jobs_library_id ON scan_jobs(library_id);
CREATE INDEX IF NOT EXISTS idx_scan_jobs_created_at ON scan_jobs(created_at);

CREATE TABLE IF NOT EXISTS auth_users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS auth_user_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  user_id INTEGER NOT NULL REFERENCES auth_users(id) ON DELETE CASCADE,
  public_key TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(user_id, public_key)
);

CREATE INDEX IF NOT EXISTS idx_auth_user_keys_user_id ON auth_user_keys(user_id);

CREATE TABLE IF NOT EXISTS lyrics_cache (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  track_id INTEGER NOT NULL REFERENCES music_tracks(id) ON DELETE CASCADE,
  provider_key TEXT NOT NULL DEFAULT '',
  lyrics_text TEXT NOT NULL DEFAULT '',
  synced_lyrics TEXT NOT NULL DEFAULT '',
  has_synced INTEGER NOT NULL DEFAULT 0,
  provider_track_id INTEGER NOT NULL DEFAULT 0,
  match_track TEXT NOT NULL DEFAULT '',
  match_artist TEXT NOT NULL DEFAULT '',
  match_album TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(track_id, provider_key)
);

-- Runtime-mutable application settings (key/value). Currently holds the
-- user-configurable TMDB API key ('tmdb_api_key'); a row overrides the env
-- default, and deleting it reverts to the env value.
CREATE TABLE IF NOT EXISTS app_settings (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL DEFAULT ''
);

-- Out-of-catalog artists (a global cache, like people for TV): the bio/image/
-- releases shown on the dedicated page for a "Similar Artist" the user doesn't
-- own. Keyed by MusicBrainz artist MBID. image_url is an external hotlink (NOT
-- downloaded to disk), releases_json is a cached []ReleaseGroupBrief. fetched_at
-- gates the lazy one-time background fetch.
CREATE TABLE IF NOT EXISTS external_artists (
  mbid TEXT PRIMARY KEY,
  name TEXT NOT NULL DEFAULT '',
  comment TEXT NOT NULL DEFAULT '',
  bio TEXT NOT NULL DEFAULT '',
  bio_source_url TEXT NOT NULL DEFAULT '',
  image_url TEXT NOT NULL DEFAULT '',
  releases_json TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL DEFAULT ''
);

-- Year-journey ("Rediscover a Year"): a walled-off discovery list of the
-- Billboard-charting albums/singles of a given year, kept entirely separate
-- from music_albums/music_tracks so the real library views/counts/search are
-- untouched. Ownership, listened-progress, and chronological order are derived
-- at view time (reconciled against the library by release-group MBID then
-- normalized title+artist; progress from play_history). One row per built year.
CREATE TABLE IF NOT EXISTS year_journeys (
  year INTEGER PRIMARY KEY,
  status TEXT NOT NULL DEFAULT 'building', -- building | ready
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  built_at TEXT NOT NULL DEFAULT ''
);

-- One acquire-target per row: an artist's album released that year, or (when the
-- act had no album that year) their top charting single. art_url is a hotlinked
-- Cover Art Archive thumbnail for albums (not downloaded), '' for singles.
-- release_date is the MB first-release-date (albums; may be year-only) or the
-- single's chart-debut date. chart_peak is the act's best Hot 100 peak that year.
CREATE TABLE IF NOT EXISTS year_journey_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  year INTEGER NOT NULL REFERENCES year_journeys(year) ON DELETE CASCADE,
  kind TEXT NOT NULL DEFAULT 'album', -- album | single
  artist_name TEXT NOT NULL DEFAULT '',
  artist_mbid TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  rg_mbid TEXT NOT NULL DEFAULT '',
  release_date TEXT NOT NULL DEFAULT '',
  chart_peak INTEGER NOT NULL DEFAULT 0,
  art_url TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_year_journey_items_year ON year_journey_items(year);

-- Cache of song → YouTube video-id lookups (YouTube Data API search), so a
-- charting song the user doesn't own is resolved at most once. Keyed by a
-- normalized "artist|title". video_id '' is a cached miss (no embeddable hit /
-- quota error) → the UI link-outs instead. Powers in-app YouTube playback on
-- the year-journey page.
CREATE TABLE IF NOT EXISTS youtube_lookups (
  query_key TEXT PRIMARY KEY,
  video_id TEXT NOT NULL DEFAULT '',
  fetched_at TEXT NOT NULL DEFAULT (datetime('now'))
);
`

func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schemaSQL); err != nil {
		return err
	}
	if err := ensureColumn(db, "tv_series_identities", "guessed_title", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "tv_series_identities", "air_date", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// art_checked_at records the last time the cover-art re-fetch pass probed a
	// matched-but-art-less album, so genuinely art-less albums aren't re-probed
	// on every match run (a TTL re-sweep retries them since CAA accrues art).
	if err := ensureColumn(db, "music_albums", "art_checked_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// popularity is a per-track global listen count from ListenBrainz (0 =
	// unknown/unmatched), filled by the music-match popularity phase and used to
	// rank the "Most Popular" shuffle playlist.
	if err := ensureColumn(db, "music_tracks", "popularity", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// similar_json caches the ListenBrainz similar-artists list (a []SimilarArtist)
	// for the artist page; similar_fetched_at gates the lazy one-time fetch so a
	// cache-miss view doesn't re-enqueue on every render.
	if err := ensureColumn(db, "music_artists", "similar_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "music_artists", "similar_fetched_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// movie_files: the scanner parses guessed_title/year from the path (the matcher
	// reads them); tmdb_id is the matched identity linking to movie_art and the
	// metadata cache. Added here for DBs that created movie_files before these
	// columns existed (the canonical CREATE TABLE in schemaSQL carries them too).
	if err := ensureColumn(db, "movie_files", "guessed_title", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "movie_files", "year", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureColumn(db, "movie_files", "tmdb_id", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := migrateIdentitiesSkippedStatus(db); err != nil {
		return err
	}
	if err := migrateUncertainToUnmatched(db); err != nil {
		return err
	}
	if err := migrateIdentitiesMatchedUnmatched(db); err != nil {
		return err
	}
	return nil
}

// migrateUncertainToUnmatched converts any music_albums rows with
// match_status='uncertain' to 'unmatched'. The uncertain state has been
// eliminated in favor of a simpler two-state model (matched or unmatched).
// Naturally idempotent: no rows match after the first run.
func migrateUncertainToUnmatched(db *sql.DB) error {
	_, err := db.Exec("UPDATE music_albums SET match_status='unmatched' WHERE match_status='uncertain'")
	return err
}

// migrateIdentitiesSkippedStatus adds 'skipped' to the tv_series_identities.status CHECK constraint.
// SQLite can't ALTER a CHECK, so we recreate the table. Idempotent: skips if already present.
func migrateIdentitiesSkippedStatus(db *sql.DB) error {
	var createSQL string
	err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='tv_series_identities'").Scan(&createSQL)
	if err != nil {
		return nil // table doesn't exist yet (fresh install handled by schemaSQL)
	}
	if strings.Contains(createSQL, "'skipped'") {
		return nil // already migrated
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`CREATE TABLE tv_series_identities_new (
  file_id INTEGER PRIMARY KEY REFERENCES tv_series_files(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'unmatched' CHECK(status IN ('matched','unmatched','skipped')),
  provider TEXT NOT NULL DEFAULT '',
  series_id TEXT NOT NULL DEFAULT '',
  season_number INTEGER NOT NULL DEFAULT -1,
  episode_numbers_csv TEXT NOT NULL DEFAULT '',
  match_confidence REAL NOT NULL DEFAULT 0.0,
  match_method TEXT NOT NULL DEFAULT '',
  matched_at TEXT NOT NULL DEFAULT '',
  guessed_title TEXT NOT NULL DEFAULT ''
)`)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`INSERT INTO tv_series_identities_new
SELECT file_id, status, provider, series_id, season_number, episode_numbers_csv,
       match_confidence, match_method, matched_at, guessed_title
FROM tv_series_identities`)
	if err != nil {
		return err
	}

	if _, err = tx.Exec("DROP TABLE tv_series_identities"); err != nil {
		return err
	}
	if _, err = tx.Exec("ALTER TABLE tv_series_identities_new RENAME TO tv_series_identities"); err != nil {
		return err
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS idx_tv_series_identities_provider_series ON tv_series_identities(provider, series_id)"); err != nil {
		return err
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS idx_tv_series_identities_status ON tv_series_identities(status)"); err != nil {
		return err
	}

	return tx.Commit()
}

// migrateIdentitiesMatchedUnmatched converts tv_series_identities status values
// from resolved/needs_fix to matched/unmatched. SQLite can't ALTER a CHECK, so
// we recreate the table. Idempotent: skips if 'matched' already in CREATE SQL.
func migrateIdentitiesMatchedUnmatched(db *sql.DB) error {
	var createSQL string
	err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='tv_series_identities'").Scan(&createSQL)
	if err != nil {
		return nil // table doesn't exist yet (fresh install handled by schemaSQL)
	}
	if strings.Contains(createSQL, "'matched'") {
		return nil // already migrated
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`CREATE TABLE tv_series_identities_new (
  file_id INTEGER PRIMARY KEY REFERENCES tv_series_files(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'unmatched' CHECK(status IN ('matched','unmatched','skipped')),
  provider TEXT NOT NULL DEFAULT '',
  series_id TEXT NOT NULL DEFAULT '',
  season_number INTEGER NOT NULL DEFAULT -1,
  episode_numbers_csv TEXT NOT NULL DEFAULT '',
  match_confidence REAL NOT NULL DEFAULT 0.0,
  match_method TEXT NOT NULL DEFAULT '',
  matched_at TEXT NOT NULL DEFAULT '',
  guessed_title TEXT NOT NULL DEFAULT ''
)`)
	if err != nil {
		return err
	}

	_, err = tx.Exec(`INSERT INTO tv_series_identities_new
SELECT file_id,
       CASE status WHEN 'resolved' THEN 'matched' WHEN 'needs_fix' THEN 'unmatched' ELSE status END,
       provider, series_id, season_number, episode_numbers_csv,
       match_confidence, match_method, matched_at, guessed_title
FROM tv_series_identities`)
	if err != nil {
		return err
	}

	if _, err = tx.Exec("DROP TABLE tv_series_identities"); err != nil {
		return err
	}
	if _, err = tx.Exec("ALTER TABLE tv_series_identities_new RENAME TO tv_series_identities"); err != nil {
		return err
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS idx_tv_series_identities_provider_series ON tv_series_identities(provider, series_id)"); err != nil {
		return err
	}
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS idx_tv_series_identities_status ON tv_series_identities(status)"); err != nil {
		return err
	}

	return tx.Commit()
}

var safeIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func ensureColumn(db *sql.DB, table, col, decl string) error {
	if !safeIdentifier.MatchString(table) {
		return fmt.Errorf("ensureColumn: invalid table name %q", table)
	}
	if !safeIdentifier.MatchString(col) {
		return fmt.Errorf("ensureColumn: invalid column name %q", col)
	}
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == col {
			return nil
		}
	}

	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + col + " " + decl)
	return err
}
