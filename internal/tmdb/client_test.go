package tmdb

import "testing"

const sampleSearchJSON = `{
  "page": 1,
  "results": [
    {
      "id": 1396,
      "name": "Breaking Bad",
      "first_air_date": "2008-01-20",
      "overview": "A high school chemistry teacher diagnosed with inoperable lung cancer.",
      "poster_path": "/ggFHVNu6YYI5L9pCfOacjizRGt.jpg",
      "popularity": 234.5
    },
    {
      "id": 999,
      "name": "Breaking Bad: Criminal Elements",
      "first_air_date": "",
      "overview": "Some other show",
      "poster_path": "",
      "popularity": 12.3
    }
  ],
  "total_results": 2
}`

const sampleShowJSON = `{
  "id": 1396,
  "name": "Breaking Bad",
  "overview": "A teacher turns to manufacturing meth.",
  "first_air_date": "2008-01-20",
  "poster_path": "/ggFHVNu6YYI5L9pCfOacjizRGt.jpg",
  "backdrop_path": "/tsRy63Mu5cu8etL1X7ZLyf7UP1M.jpg",
  "status": "Ended",
  "genres": [
    {"id": 18, "name": "Drama"},
    {"id": 80, "name": "Crime"}
  ],
  "seasons": [
    {"season_number": 0, "name": "Specials", "poster_path": ""},
    {"season_number": 1, "name": "Season 1", "poster_path": "/1BP4xYv9ZG4ZVHkL7ocOziBbSYH.jpg", "air_date": "2008-01-20"}
  ]
}`

const sampleSeasonJSON = `{
  "season_number": 1,
  "name": "Season 1",
  "overview": "First season.",
  "poster_path": "/1BP4xYv9ZG4ZVHkL7ocOziBbSYH.jpg",
  "air_date": "2008-01-20",
  "episodes": [
    {
      "episode_number": 1,
      "name": "Pilot",
      "overview": "Walter White starts cooking.",
      "still_path": "/ydlY3iPfeOAvu8gVqrxPoMvzNCn.jpg",
      "air_date": "2008-01-20",
      "vote_average": 8.1
    },
    {
      "episode_number": 2,
      "name": "Cat's in the Bag...",
      "overview": "Episode 2.",
      "still_path": "/abc.jpg",
      "air_date": "2008-01-27",
      "vote_average": 7.9
    }
  ]
}`

func TestParseSearchResponse(t *testing.T) {
	results, err := parseSearchResponse([]byte(sampleSearchJSON))
	if err != nil {
		t.Fatalf("parseSearchResponse: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].ID != 1396 {
		t.Fatalf("results[0].ID = %d, want 1396", results[0].ID)
	}
	if results[0].Name != "Breaking Bad" {
		t.Fatalf("results[0].Name = %q", results[0].Name)
	}
	if results[0].Popularity != 234.5 {
		t.Fatalf("results[0].Popularity = %v", results[0].Popularity)
	}
	if results[0].PosterPath != "/ggFHVNu6YYI5L9pCfOacjizRGt.jpg" {
		t.Fatalf("results[0].PosterPath = %q", results[0].PosterPath)
	}
}

func TestParseShowResponse(t *testing.T) {
	show, err := parseShowResponse([]byte(sampleShowJSON))
	if err != nil {
		t.Fatalf("parseShowResponse: %v", err)
	}
	if show.ID != 1396 {
		t.Fatalf("show.ID = %d, want 1396", show.ID)
	}
	if show.Name != "Breaking Bad" {
		t.Fatalf("show.Name = %q", show.Name)
	}
	if show.Status != "Ended" {
		t.Fatalf("show.Status = %q", show.Status)
	}
	if len(show.Genres) != 2 {
		t.Fatalf("genres = %d, want 2", len(show.Genres))
	}
	if show.Genres[0].Name != "Drama" {
		t.Fatalf("genres[0] = %q", show.Genres[0].Name)
	}
	if len(show.Seasons) != 2 {
		t.Fatalf("seasons = %d, want 2", len(show.Seasons))
	}
	if show.Seasons[1].SeasonNumber != 1 {
		t.Fatalf("seasons[1].SeasonNumber = %d", show.Seasons[1].SeasonNumber)
	}
}

func TestParseSeasonResponse(t *testing.T) {
	season, err := parseSeasonResponse([]byte(sampleSeasonJSON))
	if err != nil {
		t.Fatalf("parseSeasonResponse: %v", err)
	}
	if season.SeasonNumber != 1 {
		t.Fatalf("season_number = %d, want 1", season.SeasonNumber)
	}
	if len(season.Episodes) != 2 {
		t.Fatalf("episodes = %d, want 2", len(season.Episodes))
	}
	ep := season.Episodes[0]
	if ep.EpisodeNumber != 1 {
		t.Fatalf("ep[0].EpisodeNumber = %d", ep.EpisodeNumber)
	}
	if ep.Name != "Pilot" {
		t.Fatalf("ep[0].Name = %q", ep.Name)
	}
	if ep.VoteAverage != 8.1 {
		t.Fatalf("ep[0].VoteAverage = %v", ep.VoteAverage)
	}
}
