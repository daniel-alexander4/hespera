# Milestone Context: v1.3

**Gathered:** 2026-03-07
**Status:** Questioning in progress (needs confirmation)

## What the User Wants

### Problem 1: Albums without artwork
- No manual way to add/upload cover art to albums that didn't get art from Cover Art Archive
- Need: manual artwork upload or assignment capability

### Problem 2: Misnumbered tracks
- Track numbering is wrong on some albums
- Note: Single-track edit UI already exists (album edit page, single mode) — can edit track_no, disc_no per track
- May need: better discoverability, bulk renumbering, or the existing UI might be sufficient

### Problem 3: Rematch loop (bug/design gap)
- User rematches an album → handler clears match_status to '' and wipes MBID/confidence
- Background job runs RunMusicMatch → same 80% threshold applies
- If MusicBrainz returns same candidates with score < 80 → album becomes 'unmatched' again
- **Root cause:** No way to manually accept a below-threshold match result
- Need: Manual match acceptance — user sees candidates, picks one, regardless of score

## Current Edit Capabilities (handlers_music.go:630-777)
- Album-level: title, artist, year (writes tags + rescans)
- Single-track: title, artist, track_no, disc_no (writes tags + rescans)
- Sets match_status='manual' after edit
- Does NOT handle: artwork, manual match selection

## Scope Discussion Needed
- Is this purely music library management improvements?
- Should TV have similar manual override capabilities?
- What about the rematch loop — is it a bug fix or a new feature (manual match selection UI)?

## Out of Scope (from PROJECT.md)
- Movie scanning — separate milestone
- Major refactoring — architectural debt
- Performance optimization — separate milestone
