package match

import "math"

// matchThreshold is the minimum ScoreCandidate result (max ~96) for a candidate
// to be accepted as a match; anything below is treated as unmatched. There is a
// single cutoff — the former "uncertain" tier was retired (see db.Migrate).
const matchThreshold = 80

// ScoreCandidate scores a MusicBrainz candidate against local album metadata.
// Max score is ~96.
func ScoreCandidate(c Candidate, localTitle, localArtist string, localYear int) float64 {
	titleSim := NormalizedSimilarity(c.Title, localTitle)
	artistSim := NormalizedSimilarity(c.ArtistName, localArtist)

	titleScore := titleSim * 38.0
	artistScore := artistSim * 26.0
	mbScore := float64(c.MBScore) / 100.0 * 18.0
	typeScore := typeBonus(c.PrimaryType, c.SecondaryTypes)
	yearScore := yearBonus(c.Year, localYear)

	return titleScore + artistScore + mbScore + typeScore + yearScore
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
		switch st {
		case "Compilation", "Live", "Remix", "Demo", "Interview", "DJ-mix", "Mixtape/Street":
			base -= 6
		}
	}
	if base < 0 {
		base = 0
	}
	return base
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
