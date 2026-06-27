package match

import (
	"math"
	"sort"
)

// matchThreshold is the minimum ScoreCandidate result (max ~96) for a candidate
// to be accepted as a match; anything below is treated as unmatched. There is a
// single cutoff — the former "uncertain" tier was retired (see db.Migrate).
const matchThreshold = 80

// ScoreCandidate scores a MusicBrainz candidate against local album metadata.
// Max score is ~96.
func ScoreCandidate(c Candidate, localTitle, localArtist string, localYear int) float64 {
	titleSim := bestTitleSim(c, localTitle)
	artistSim := NormalizedSimilarity(c.ArtistName, localArtist)

	titleScore := titleSim * 38.0
	artistScore := artistSim * 26.0
	mbScore := float64(c.MBScore) / 100.0 * 18.0
	typeScore := typeBonus(c.PrimaryType, c.SecondaryTypes)
	yearScore := yearBonus(c.Year, localYear)

	return titleScore + artistScore + mbScore + typeScore + yearScore
}

// bestTitleSim returns the highest title similarity between the local title and
// the candidate's canonical title or any of its aliases, comparing after
// NormalizeTitle (so annotations like remaster/deluxe/live-date are stripped on
// both sides — the canonical normalizer is the single source of truth). Aliases
// let an album filed under one regional title match local files tagged with the
// other (e.g. MB "Killing Machine" vs local "Hell Bent for Leather").
func bestTitleSim(c Candidate, localTitle string) float64 {
	lt := NormalizeTitle(localTitle)
	best := NormalizedSimilarity(NormalizeTitle(c.Title), lt)
	for _, a := range c.Aliases {
		if s := NormalizedSimilarity(NormalizeTitle(a), lt); s > best {
			best = s
		}
	}
	return best
}

// typeBonus rewards the canonical studio album and penalizes the alternate
// editions (singles, EPs, live/compilation/remix release-groups) that share an
// album's title but usually lack canonical cover art. Penalizing them lets a
// clean primary=Album / no-secondary release-group win among same-titled
// candidates, so the matcher selects the release-group that actually has art.
// Title and artist similarity dominate the overall score, so this only reorders
// same-titled siblings — it does not unmatch a strong title/artist match.
func typeBonus(primaryType string, secondaryTypes []string) float64 {
	base := 10.0
	switch primaryType {
	case "Single":
		base -= 8
	case "EP":
		base -= 4
	case "Broadcast":
		base -= 6
	case "Live", "Remix", "Compilation":
		base -= 6
	}
	// Secondary types mark non-primary editions even when the primary type is
	// Album (e.g. a live or greatest-hits album). Soundtrack/Spokenword are
	// legitimate primary uses and are intentionally not penalized.
	for _, st := range secondaryTypes {
		if altSecondaryTypes[st] {
			base -= 6
		}
	}
	if base < 0 {
		base = 0
	}
	return base
}

// altSecondaryTypes are MusicBrainz release-group secondary types that mark a
// non-primary edition (greatest-hits, live, demos), which usually carries
// different — or no — cover art than the canonical studio album.
var altSecondaryTypes = map[string]bool{
	"Compilation": true, "Live": true, "Remix": true, "Demo": true,
	"Interview": true, "DJ-mix": true, "Mixtape/Street": true,
}

// isCleanAlbum reports whether a candidate is a canonical studio album — a
// primary type of Album with no alternate-edition secondary type. Used to gate
// which sibling release-groups may donate cover art to a matched album: only a
// clean same-album edition's cover is safe to reuse.
func isCleanAlbum(c Candidate) bool {
	if c.PrimaryType != "Album" {
		return false
	}
	for _, st := range c.SecondaryTypes {
		if altSecondaryTypes[st] {
			return false
		}
	}
	return true
}

func yearBonus(mbYear, localYear int) float64 {
	if mbYear == 0 || localYear == 0 {
		return 0
	}
	diff := int(math.Abs(float64(mbYear - localYear)))
	switch {
	case diff <= 1:
		return 4
	case diff <= 3:
		return 2
	default:
		return 0
	}
}

// typeDemotion is how many points typeBonus subtracted from a clean studio
// album's full bonus (10) for this candidate's edition type — i.e. the penalty
// the non-demoting match threshold ignores. 0 for a clean Album/Soundtrack.
func typeDemotion(primaryType string, secondaryTypes []string) float64 {
	return 10.0 - typeBonus(primaryType, secondaryTypes)
}

// BestMatchCandidate chooses the release-group to match the local album to,
// applying a NON-DEMOTING edition penalty. A candidate is eligible when its
// score clears matchThreshold treating its type as a clean studio album
// (`ScoreCandidate + typeDemotion >= matchThreshold`), so the edition-type
// penalty can reorder same-titled siblings but never unmatch a strong
// title/artist/year match (e.g. a real Live album like "Made in Japan"). Among
// eligible candidates the highest *actual* ScoreCandidate wins, so the penalty
// still steers the pick toward the canonical album that carries cover art.
//
// This is a strict superset of BestCandidate's matches at the threshold: every
// candidate that cleared `ScoreCandidate >= matchThreshold` is still eligible
// (its demotion is >= 0), and the winner among eligibles is the same ranking —
// only near-threshold secondary editions are rescued from a spurious unmatch.
func BestMatchCandidate(candidates []Candidate, localTitle, localArtist string, localYear int) (Candidate, float64, bool) {
	bestIdx := -1
	bestScore := 0.0
	for i, c := range candidates {
		s := ScoreCandidate(c, localTitle, localArtist, localYear)
		if s+typeDemotion(c.PrimaryType, c.SecondaryTypes) < matchThreshold {
			continue // not a match even crediting it the full clean-album type bonus
		}
		if bestIdx == -1 || s > bestScore {
			bestIdx, bestScore = i, s
		}
	}
	if bestIdx == -1 {
		return Candidate{}, 0, false
	}
	return candidates[bestIdx], bestScore, true
}

// BestCandidate picks the highest-scoring candidate. Returns the candidate,
// its score, and whether any candidate was found.
func BestCandidate(candidates []Candidate, localTitle, localArtist string, localYear int) (Candidate, float64, bool) {
	if len(candidates) == 0 {
		return Candidate{}, 0, false
	}
	bestIdx := 0
	bestScore := ScoreCandidate(candidates[0], localTitle, localArtist, localYear)
	for i := 1; i < len(candidates); i++ {
		s := ScoreCandidate(candidates[i], localTitle, localArtist, localYear)
		if s > bestScore {
			bestScore = s
			bestIdx = i
		}
	}
	return candidates[bestIdx], bestScore, true
}

// ScoredCandidate pairs a candidate with its computed score.
type ScoredCandidate struct {
	Candidate Candidate
	Score     float64
}

// CandidatesAboveThreshold returns every candidate scoring >= matchThreshold,
// sorted by score descending. The first element matches BestCandidate's pick.
// Used to broaden cover-art search beyond the single best match.
func CandidatesAboveThreshold(candidates []Candidate, localTitle, localArtist string, localYear int) []ScoredCandidate {
	var out []ScoredCandidate
	for _, c := range candidates {
		s := ScoreCandidate(c, localTitle, localArtist, localYear)
		if s >= matchThreshold {
			out = append(out, ScoredCandidate{Candidate: c, Score: s})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}
