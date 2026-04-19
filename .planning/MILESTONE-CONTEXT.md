# Milestone Context: v1.3

**Gathered:** 2026-03-07
**Status:** Ready for requirements

## What the User Wants

### Problem 1: Albums without artwork
- No manual way to add/upload cover art to albums that didn't get art from Cover Art Archive
- Need: manual artwork upload or assignment capability

### Problem 2: Track numbering broken (BUG)
- Single-track edit UI exists (album edit page, single mode) but track numbering is NOT WORKING
- User confirmed: editing track_no via the UI does not work correctly
- Need: fix the track numbering edit, verify it writes correct values to tags and DB

### Problem 3: Rematch loop → Manual match selection
- User rematches an album → handler clears match_status to '' and wipes MBID/confidence
- Background job runs RunMusicMatch → same 80% threshold applies
- If MusicBrainz returns same candidates with score < 80 → album becomes 'unmatched' again
- **Root cause:** No way to manually accept a below-threshold match result
- **Solution confirmed:** Manual match selection UI — user sees candidates from MusicBrainz/TMDB, picks one regardless of score
- Applies to BOTH music and TV

### Scope: Both music AND TV
- Manual match selection for both music (MusicBrainz) and TV (TMDB)
- Manual artwork for both where applicable
- Track numbering fix is music-only

## Current Edit Capabilities (handlers_music.go:630-777)
- Album-level: title, artist, year (writes tags + rescans)
- Single-track: title, artist, track_no, disc_no (writes tags + rescans) — TRACK_NO NOT WORKING
- Sets match_status='manual' after edit
- Does NOT handle: artwork, manual match selection

## Technical Notes
- Rematch handler: handlers_match.go:240-270 (clears match_status to '')
- Match pipeline: pipeline.go:32-294 (RunMusicMatch)
- Scoring: scorer.go:231-236 (80% threshold in matchAlbum)
- Album query for matching: match_status='' OR match_status='unmatched' (pipeline.go:156)
- TV match: internal/tmdb/matcher.go (0.80 threshold at line 107)

## Out of Scope
- Movie scanning — separate milestone
- Major refactoring — architectural debt
- Performance optimization — separate milestone
