package match

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"hespera/internal/ratelimit"
)

func TestNewClientsNilWithoutKey(t *testing.T) {
	if NewFanartClient("") != nil {
		t.Fatal("FanartClient should be nil without a key")
	}
	if NewAudioDBClient("") != nil {
		t.Fatal("AudioDBClient should be nil without a key")
	}
}

func TestFanartArtistImageURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artistthumb":[{"url":"https://img/thumb.jpg","likes":"5"}],"artistbackground":[{"url":"https://img/bg.jpg"}]}`))
	}))
	defer srv.Close()

	c := NewFanartClient("k")
	c.baseURL = srv.URL
	c.limiter = ratelimit.New(0)

	if got := c.ArtistImageURL(context.Background(), "mbid-1"); got != "https://img/thumb.jpg" {
		t.Fatalf("ArtistImageURL = %q, want the thumb (preferred over background)", got)
	}
	// A nil client is a safe no-op.
	var nilC *FanartClient
	if got := nilC.ArtistImageURL(context.Background(), "mbid-1"); got != "" {
		t.Fatalf("nil client returned %q, want empty", got)
	}
}

func TestAudioDBArtistBioAndImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"artists":[{"strBiographyEN":"A short bio.","strArtistThumb":"https://img/a.jpg"}]}`))
	}))
	defer srv.Close()

	c := NewAudioDBClient("k")
	c.baseURL = srv.URL
	c.limiter = ratelimit.New(0)

	if got := c.ArtistBio(context.Background(), "mbid-1"); got != "A short bio." {
		t.Fatalf("ArtistBio = %q", got)
	}
	if got := c.ArtistImageURL(context.Background(), "mbid-1"); got != "https://img/a.jpg" {
		t.Fatalf("ArtistImageURL = %q", got)
	}
}

func TestBackfillEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	f := NewFanartClient("k")
	f.baseURL, f.limiter = srv.URL, ratelimit.New(0)
	if got := f.ArtistImageURL(context.Background(), "x"); got != "" {
		t.Fatalf("fanart empty resp = %q, want empty", got)
	}
	a := NewAudioDBClient("k")
	a.baseURL, a.limiter = srv.URL, ratelimit.New(0)
	if got := a.ArtistBio(context.Background(), "x"); got != "" {
		t.Fatalf("audiodb empty resp = %q, want empty", got)
	}
}
