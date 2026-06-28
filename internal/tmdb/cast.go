package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
)

// castLimit caps how many cast members we store per title, by billing order, so
// a large ensemble doesn't make an unwieldy strip or a burst of image downloads.
const castLimit = 15

// filmographyLimit caps how many of an actor's shows we cache/show on their page.
const filmographyLimit = 24

// topTVCredits dedupes an actor's tv_credits by show (keeping the entry with the
// most episodes), orders by significance (episode count, then recency), and caps
// the list.
func topTVCredits(credits []PersonTVCredit) []PersonTVCredit {
	byID := make(map[int]PersonTVCredit, len(credits))
	for _, c := range credits {
		if c.ID == 0 || c.Name == "" {
			continue
		}
		if ex, ok := byID[c.ID]; !ok || c.EpisodeCount > ex.EpisodeCount {
			byID[c.ID] = c
		}
	}
	out := make([]PersonTVCredit, 0, len(byID))
	for _, c := range byID {
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].EpisodeCount != out[j].EpisodeCount {
			return out[i].EpisodeCount > out[j].EpisodeCount
		}
		return out[i].FirstAirDate > out[j].FirstAirDate
	})
	if len(out) > filmographyLimit {
		out = out[:filmographyLimit]
	}
	return out
}

// FetchTVCast fetches a matched show's top-billed cast from TMDB and caches it:
// the `people` rows (name + downloaded profile image), the `credits` join
// (character + billing order), and a `show:%d:cast` marker in
// tv_series_metadata_cache so the lazy backfill knows the fetch ran even when a
// show has no cast (and so it isn't re-run on every page view). Best-effort per
// person; image downloads happen outside the DB transaction.
func (m *Matcher) FetchTVCast(ctx context.Context, showID int) error {
	if err := os.MkdirAll(m.artDir, 0o755); err != nil {
		return err
	}
	cast, err := m.client.FetchTVAggregateCredits(ctx, showID)
	if err != nil {
		return err
	}
	sort.SliceStable(cast, func(i, j int) bool { return cast[i].Order < cast[j].Order })
	if len(cast) > castLimit {
		cast = cast[:castLimit]
	}

	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	// Replace this show's cast wholesale so a trimmed/changed cast leaves no stale rows.
	if _, err := tx.ExecContext(ctx, "DELETE FROM credits WHERE media_type='tv' AND media_id=?", showID); err != nil {
		tx.Rollback()
		return err
	}
	for _, cm := range cast {
		character := ""
		if len(cm.Roles) > 0 {
			character = cm.Roles[0].Character
		}
		// Upsert the person, preserving any bio/art already fetched.
		if _, err := tx.ExecContext(ctx, `
INSERT INTO people (tmdb_id, name, profile_path) VALUES (?, ?, ?)
ON CONFLICT(tmdb_id) DO UPDATE SET name=excluded.name, profile_path=excluded.profile_path, updated_at=datetime('now')
`, cm.ID, cm.Name, cm.ProfilePath); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO credits (person_id, media_type, media_id, character_name, billing_order)
VALUES (?, 'tv', ?, ?, ?)
`, cm.ID, showID, character, cm.Order); err != nil {
			tx.Rollback()
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO tv_series_metadata_cache (entity_key, lang, payload_json, fetched_at)
VALUES (?, 'en', '{}', datetime('now'))
ON CONFLICT(entity_key, lang) DO UPDATE SET fetched_at=excluded.fetched_at, updated_at=datetime('now')
`, fmt.Sprintf("show:%d:cast", showID)); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Download profile images (best-effort) for members lacking one.
	for _, cm := range cast {
		if cm.ProfilePath == "" {
			continue
		}
		var ap string
		_ = m.db.QueryRowContext(ctx, "SELECT art_path FROM people WHERE tmdb_id=?", cm.ID).Scan(&ap)
		if ap != "" {
			continue
		}
		dest := filepath.Join(m.personImageDir(), fmt.Sprintf("person_%d_profile.jpg", cm.ID))
		if err := m.client.DownloadImage(ctx, cm.ProfilePath, dest); err != nil {
			slog.Warn("tmdb profile download", "person", cm.ID, "err", err)
			continue
		}
		_, _ = m.db.ExecContext(ctx, "UPDATE people SET art_path=? WHERE tmdb_id=?", dest, cm.ID)
	}
	return nil
}

// FetchPersonBio fetches and caches a person's biography (and profile image if
// not already on disk) from /person/{id}. Lazy: enqueued when an actor page is
// first viewed.
func (m *Matcher) FetchPersonBio(ctx context.Context, personID int) error {
	if err := os.MkdirAll(m.artDir, 0o755); err != nil {
		return err
	}
	p, err := m.client.FetchPerson(ctx, personID)
	if err != nil {
		return err
	}
	if _, err := m.db.ExecContext(ctx, `
INSERT INTO people (tmdb_id, name, profile_path, bio, bio_fetched_at)
VALUES (?, ?, ?, ?, datetime('now'))
ON CONFLICT(tmdb_id) DO UPDATE SET
  bio=excluded.bio,
  bio_fetched_at=excluded.bio_fetched_at,
  name=CASE WHEN people.name='' THEN excluded.name ELSE people.name END,
  profile_path=CASE WHEN people.profile_path='' THEN excluded.profile_path ELSE people.profile_path END,
  updated_at=datetime('now')
`, p.ID, p.Name, p.ProfilePath, p.Biography); err != nil {
		return err
	}
	var ap string
	_ = m.db.QueryRowContext(ctx, "SELECT art_path FROM people WHERE tmdb_id=?", personID).Scan(&ap)
	if ap == "" && p.ProfilePath != "" {
		dest := filepath.Join(m.personImageDir(), fmt.Sprintf("person_%d_profile.jpg", personID))
		if err := m.client.DownloadImage(ctx, p.ProfilePath, dest); err == nil {
			_, _ = m.db.ExecContext(ctx, "UPDATE people SET art_path=? WHERE tmdb_id=?", dest, personID)
		}
	}

	// Cache the actor's TV filmography (best-effort) — powers the "Other shows"
	// (not-in-library) section. Posters for those are hotlinked client-side, so
	// nothing is downloaded here.
	if credits, err := m.client.FetchPersonTVCredits(ctx, personID); err == nil {
		data, _ := json.Marshal(topTVCredits(credits))
		_, _ = m.db.ExecContext(ctx, "UPDATE people SET filmography_json=? WHERE tmdb_id=?", string(data), personID)
	} else {
		slog.Warn("tmdb person tv credits", "person", personID, "err", err)
	}
	return nil
}
