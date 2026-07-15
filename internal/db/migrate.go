package db

import (
	"context"
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
  type TEXT NOT NULL CHECK(type IN ('music','movies','tv','home_media')),
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

CREATE TABLE IF NOT EXISTS playlists (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now')),
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

-- User-curated playlist membership. PK (playlist_id, track_id) = no duplicate
-- songs in one playlist (adds are idempotent); ordering via position, renumbered
-- contiguously on remove/reorder (no UNIQUE on position — a one-statement swap
-- would trip it mid-transaction). Track deletion cascades the membership away;
-- the move-relink pass re-points track_id like play_history/lyrics_cache.
CREATE TABLE IF NOT EXISTS playlist_tracks (
  playlist_id INTEGER NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
  track_id INTEGER NOT NULL REFERENCES music_tracks(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  added_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (playlist_id, track_id)
);
CREATE INDEX IF NOT EXISTS idx_playlist_tracks_position ON playlist_tracks(playlist_id, position);

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
-- Serves the recently-played aggregate (SELECT artist_id, MAX(created_at) ...
-- WHERE library_id=? GROUP BY artist_id) rendered on the music home + landing
-- pages: a covering index so its cost scales with distinct artists, not lifetime
-- plays (without it the grouped MAX is a full-table scan that grows unbounded).
CREATE INDEX IF NOT EXISTS idx_play_history_lib_artist_created ON play_history(library_id, artist_id, created_at);

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

-- Detected skip ranges (intro/credits) per file, currently from cross-episode audio
-- fingerprinting. Merged with chapter/EDL markers when building a playback session.
CREATE TABLE IF NOT EXISTS tv_skip_segments (
  file_id INTEGER NOT NULL REFERENCES tv_series_files(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  start_sec REAL NOT NULL,
  end_sec REAL NOT NULL,
  source TEXT NOT NULL DEFAULT 'fingerprint',
  detected_at TEXT NOT NULL DEFAULT (datetime('now')),
  PRIMARY KEY (file_id, kind, source)
);
CREATE INDEX IF NOT EXISTS idx_tv_skip_segments_file ON tv_skip_segments(file_id);

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
  manual INTEGER NOT NULL DEFAULT 0, -- 1 = a user-uploaded override; a (re)match must not clobber it
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

-- Photos libraries: mixed still images + home-video clips, one row per file.
-- No matching/identity — photos have no metadata provider; taken_at is the
-- capture timestamp (EXIF > container creation_time > file mtime, per
-- taken_source) driving the By Date view, dir_rel the Folders grouping.
-- thumb_path: '' = pending generation, 'unavailable' = generation failed
-- (undecodable format), else the id-keyed file under DataDir/thumbs/photos.
CREATE TABLE IF NOT EXISTS photos (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  abs_path TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'photo',
  container TEXT NOT NULL DEFAULT '',
  file_size_bytes INTEGER NOT NULL DEFAULT 0,
  mtime_unix INTEGER NOT NULL DEFAULT 0,
  taken_at TEXT NOT NULL DEFAULT '',
  taken_source TEXT NOT NULL DEFAULT '',
  orientation INTEGER NOT NULL DEFAULT 0,
  stream_info_json TEXT NOT NULL DEFAULT '{}',
  dir_rel TEXT NOT NULL DEFAULT '',
  thumb_path TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT (datetime('now')),
  UNIQUE(library_id, abs_path)
);

-- The photos browse surfaces are one merged view across all photos libraries
-- (no query constrains library_id): the grid orders by (taken_at, id), the
-- viewer's prev/next is a row-value seek on the same tuple, and the Folders
-- tab groups by dir_rel — so the indexes lead with those columns.
CREATE INDEX IF NOT EXISTS idx_photos_taken_id ON photos(taken_at, id);
CREATE INDEX IF NOT EXISTS idx_photos_dir ON photos(dir_rel);

CREATE TABLE IF NOT EXISTS photo_playback_progress (
  file_id INTEGER PRIMARY KEY REFERENCES photos(id) ON DELETE CASCADE,
  position_seconds REAL NOT NULL DEFAULT 0,
  duration_seconds REAL NOT NULL DEFAULT 0,
  completed INTEGER NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS scan_jobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  library_id INTEGER NOT NULL DEFAULT 0,
  job_type TEXT NOT NULL DEFAULT 'music_scan',
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
	// year is the show's release year taken from the show folder name (e.g.
	// "Doctor Who (2023)"), used by the matcher to disambiguate reboots. 0 = none.
	if err := ensureColumn(db, "tv_series_identities", "year", "INTEGER NOT NULL DEFAULT 0"); err != nil {
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
	// loudness_lufs is the track's integrated loudness (EBU R128, from ffmpeg's
	// loudnorm analysis), filled by the music_loudness job chained after music
	// scans and used for playback volume leveling. 0 = not yet analyzed (real
	// music never measures exactly 0.0 LUFS; the analyzer nudges such a result);
	// the scanner resets it on a size/mtime change like integrity_status.
	if err := ensureColumn(db, "music_tracks", "loudness_lufs", "REAL NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// loudness_tp is the track's true peak (dBTP) from the same loudnorm pass —
	// the headroom that caps the leveling boost, so lifting a quiet track can't
	// push it past full scale. Same 0 = not-yet-analyzed sentinel (the analyzer
	// nudges a real 0.00 dBTP reading off it), which doubles as the one-shot
	// backfill marker: rows measured before this column existed carry
	// loudness_lufs<>0 with loudness_tp=0, and AnalyzeLoudness re-measures them.
	if err := ensureColumn(db, "music_tracks", "loudness_tp", "REAL NOT NULL DEFAULT 0"); err != nil {
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
	// Match-pipeline re-run TTL stamps (the art_checked_at pattern applied to the
	// three formerly-ungated phases, so an automatic chain-triggered match run is
	// near-free on an unchanged library while user-triggered runs bypass them):
	// enrich_checked_at — last enrichment attempt for a still-INCOMPLETE artist;
	// popularity_checked_at — last ListenBrainz/Last.fm popularity fetch;
	// match_checked_at — last failed MusicBrainz match attempt for an album that
	// stays unmatched (cleared by the album Unmatch reset).
	if err := ensureColumn(db, "music_artists", "enrich_checked_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "music_artists", "popularity_checked_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "music_albums", "match_checked_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
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
	// movie_art.manual marks a user-uploaded cover/backdrop so a (re)match's
	// downloadMovieArt skips it instead of overwriting. Added here for DBs that
	// created movie_art before the column existed.
	if err := ensureColumn(db, "movie_art", "manual", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	// people: filmography_json caches an actor's wider TV credits ("Other shows").
	// Added here for DBs that created people before the column existed (the canonical
	// CREATE TABLE carries it too); without it, personDetail's SELECT errors and the
	// actor page can't read the stored name/bio.
	// Episode screen-capture thumbnails: '' = pending generation (reset when
	// the file's bytes change), 'unavailable' = grab failed, else the id-keyed
	// file under DataDir/thumbs/episodes.
	if err := ensureColumn(db, "tv_series_files", "thumb_path", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "people", "filmography_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	// Local extras: files inside a title's Extras/Featurettes/Trailers/… dir are
	// ingested as playable bonus content (is_extra=1) with a filename-derived
	// display title + a dir-derived category chip. They never enter matching
	// (guessed_title stays ''), and the trickplay/integrity sweeps exclude them.
	for _, tbl := range []string{"tv_series_files", "movie_files"} {
		if err := ensureColumn(db, tbl, "is_extra", "INTEGER NOT NULL DEFAULT 0"); err != nil {
			return err
		}
		if err := ensureColumn(db, tbl, "extra_title", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		if err := ensureColumn(db, tbl, "extra_category", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	// Media integrity: per-file corruption status for video files. '' = unchecked
	// (reset to '' by the scanner when size/mtime change); 'ok' = container-clean;
	// 'repaired' = container losslessly remuxed in place; 'flagged' = unrepairable
	// damage (bitstream corruption or a remux that couldn't be safely applied);
	// 'degraded' = audio-gap-only on a sound container — missing data, but the
	// transcoder silence-fills it so the file plays cleanly (report page only,
	// not counted as corrupt).
	for _, tbl := range []string{"tv_series_files", "movie_files", "music_tracks"} {
		if err := ensureColumn(db, tbl, "integrity_status", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		if err := ensureColumn(db, tbl, "integrity_checked_at", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
		if err := ensureColumn(db, tbl, "integrity_detail", "TEXT NOT NULL DEFAULT ''"); err != nil {
			return err
		}
	}
	// Scale indexes, created here (not in schemaSQL) because they cover columns
	// added by the ensureColumn calls above — on a fresh DB those columns don't
	// exist yet when schemaSQL runs. The integrity indexes are PARTIAL (only the
	// tiny 'flagged' subset), so the Libraries page's per-load flagged-count scan
	// (integrityFlaggedCounts) touches just those rows; idx_movie_files_tmdb_id
	// backs the actor-filmography join (credits ⋈ movie_files on tmdb_id).
	if _, err := db.Exec(`
CREATE INDEX IF NOT EXISTS idx_movie_files_tmdb_id ON movie_files(tmdb_id);
CREATE INDEX IF NOT EXISTS idx_tv_series_files_flagged ON tv_series_files(library_id) WHERE integrity_status='flagged';
CREATE INDEX IF NOT EXISTS idx_movie_files_flagged ON movie_files(library_id) WHERE integrity_status='flagged';
CREATE INDEX IF NOT EXISTS idx_music_tracks_flagged ON music_tracks(library_id) WHERE integrity_status='flagged';
CREATE INDEX IF NOT EXISTS idx_tv_series_files_degraded ON tv_series_files(library_id) WHERE integrity_status='degraded';
CREATE INDEX IF NOT EXISTS idx_movie_files_degraded ON movie_files(library_id) WHERE integrity_status='degraded';
CREATE INDEX IF NOT EXISTS idx_music_tracks_degraded ON music_tracks(library_id) WHERE integrity_status='degraded';
-- Superseded photos indexes: they led with library_id, which no photos query
-- constrains (the browse is one merged view), so they served nothing. Replaced
-- by idx_photos_taken_id / idx_photos_dir in schemaSQL.
DROP INDEX IF EXISTS idx_photos_library_taken;
DROP INDEX IF EXISTS idx_photos_library_dir;
`); err != nil {
		return err
	}
	if err := migrateIntegrityDegraded(db); err != nil {
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
	if err := migrateLibraryTypeHomeMedia(db); err != nil {
		return err
	}
	// Retired feature tables — drop on existing DBs (the CREATEs are gone from the
	// schema). year_journey_*: the old "Rediscover a Year" build cache.
	// youtube_lookups/itunes_art: the removed Top-100 YouTube playback + its
	// cover-art cache. auth_user_keys/auth_users: the removed SSH-key auth layer.
	if _, err := db.Exec(`DROP TABLE IF EXISTS year_journey_items; DROP TABLE IF EXISTS year_journeys;
		DROP TABLE IF EXISTS youtube_lookups; DROP TABLE IF EXISTS itunes_art;
		DROP TABLE IF EXISTS auth_user_keys; DROP TABLE IF EXISTS auth_users;`); err != nil {
		return err
	}
	// Rename legacy scan_jobs.job_type values to the consistent <media>_<action>
	// scheme: the scan heads had ad-hoc names ('scan'/'tvscan'/…) and three jobs
	// lacked a subject prefix. Keeps the audit log + the watcher's last-scan
	// lookup consistent with the new names. Idempotent — after the rename no rows
	// match the old names.
	for _, r := range []struct{ from, to string }{
		{"scan", "music_scan"},
		{"tvscan", "tv_scan"},
		{"moviescan", "movie_scan"},
		{"photoscan", "photo_scan"},
		{"intro_detect", "tv_intro_detect"},
		{"tag_writeback", "music_tag_writeback"},
		{"tv_backdrop_refresh", "tv_backdrop_fetch"},
	} {
		if _, err := db.Exec(`UPDATE scan_jobs SET job_type=? WHERE job_type=?`, r.to, r.from); err != nil {
			return err
		}
	}
	// Refresh planner statistics so the indexes above are actually chosen (a new
	// index on a populated table can be ignored until stats catch up). PRAGMA
	// optimize is self-limiting — a cheap no-op when stats are already fresh — so
	// running it on every startup is safe; it's advisory, so ignore any error.
	_, _ = db.Exec(`PRAGMA optimize`)
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
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS idx_tv_series_identities_series_id ON tv_series_identities(series_id, status, season_number)"); err != nil {
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
	if _, err = tx.Exec("CREATE INDEX IF NOT EXISTS idx_tv_series_identities_series_id ON tv_series_identities(series_id, status, season_number)"); err != nil {
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

// migrateIntegrityDegraded reclassifies pre-split 'flagged' rows whose only
// finding was an audio gap into the 'degraded' status introduced with the
// integrity report page: audio-gap-only files play cleanly (the transcoder
// silence-fills the hole), so they are playable residue, not the unplayable
// damage the corrupt pill counts. The predicate parses only our own generated
// detail vocabulary (integrity.go writes "audio gap …" and "bitstream
// corruption …" and nothing else names either), so a decode-error file can
// never be downgraded. Idempotent: reclassified rows no longer match. A wrong
// call self-heals on the next deep check, which re-audits non-flagged rows.
func migrateIntegrityDegraded(db *sql.DB) error {
	for _, tbl := range []string{"tv_series_files", "movie_files", "music_tracks"} {
		if _, err := db.Exec(`
UPDATE ` + tbl + ` SET integrity_status='degraded',
  integrity_detail = integrity_detail || ' — playable: the transcoder silence-fills the gap; replace the file to restore the missing audio'
WHERE integrity_status='flagged'
  AND integrity_detail LIKE '%audio gap%'
  AND integrity_detail NOT LIKE '%bitstream corruption%'
  AND integrity_detail NOT LIKE '%repair failed%'
`); err != nil {
			return err
		}
	}
	return nil
}

// migrateLibraryTypeHomeMedia renames the library type 'photos' → 'home_media'
// and drops the dead scanner-less 'home_videos' placeholder, so one home-media
// type covers both home stills and clips. SQLite can't ALTER a CHECK constraint,
// so it rebuilds the libraries table with the new CHECK and maps the old values
// in the copy. FK-safe: child *_files.library_id reference libraries.id and the
// rebuild preserves every id, so foreign_keys is turned OFF for the drop/rename
// (else dropping libraries would cascade-delete its children) and the references
// stay valid against the recreated table. Done on a single pooled connection —
// PRAGMA foreign_keys is per-connection and a no-op inside a transaction.
// Idempotent: after it runs the schema no longer names 'photos', so it's skipped.
func migrateLibraryTypeHomeMedia(db *sql.DB) error {
	var ddl string
	if err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name='libraries'").Scan(&ddl); err != nil {
		return fmt.Errorf("read libraries schema: %w", err)
	}
	if !strings.Contains(ddl, "'photos'") {
		return nil // already on the new type set
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, "PRAGMA foreign_keys=ON") // restore before the conn returns to the pool
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	stmts := []string{
		`CREATE TABLE libraries_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL CHECK(type IN ('music','movies','tv','home_media')),
  root_path TEXT NOT NULL,
  created_at TEXT NOT NULL DEFAULT (datetime('now'))
)`,
		`INSERT INTO libraries_new (id, name, type, root_path, created_at)
   SELECT id, name,
     CASE type WHEN 'photos' THEN 'home_media' WHEN 'home_videos' THEN 'home_media' ELSE type END,
     root_path, created_at FROM libraries`,
		`DROP TABLE libraries`,
		`ALTER TABLE libraries_new RENAME TO libraries`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("libraries home_media rebuild: %w", err)
		}
	}
	return tx.Commit()
}
