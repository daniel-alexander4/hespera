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
	typeScore := typeBonus(c.PrimaryType)
	yearScore := yearBonus(c.Year, localYear)

	return titleScore + artistScore + mbScore + typeScore + yearScore
}

func typeBonus(primaryType string) float64 {
	base := 10.0
	switch primaryType {
	case "Single":
		base -= 8
	case "Broadcast":
		base -= 6
	case "Live", "Remix", "Compilation":
		base -= 6
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
