package web

import (
	"net/http"
)

func (h *Handler) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", h.home)
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/login", h.login)
	mux.HandleFunc("/auth/challenge", h.authChallenge)
	mux.HandleFunc("/auth/verify", h.authVerify)
	mux.HandleFunc("/auth/logout", h.authLogout)

	// Libraries
	mux.HandleFunc("/libraries", h.libraries)
	mux.HandleFunc("/libraries/new", h.librariesNew)
	mux.HandleFunc("/libraries/scan", h.librariesScan)
	mux.HandleFunc("/libraries/delete", h.librariesDelete)

	// Music browse
	mux.HandleFunc("/music", h.musicHome)
	mux.HandleFunc("/music/artist/", h.musicArtistAlbums)
	mux.HandleFunc("/music/albums", h.musicAlbums)
	mux.HandleFunc("/music/album/", h.musicAlbumTracks)
	mux.HandleFunc("/music/compilations", h.musicCompilations)
	mux.HandleFunc("/music/player", h.musicPlayer)
	mux.HandleFunc("/music/play-event", h.musicPlayEvent)

	// Streaming
	mux.HandleFunc("/stream/track/", h.streamTrack)

	// Art
	mux.HandleFunc("/art/album/", h.albumArt)
	mux.HandleFunc("/art/artist/", h.artistArt)

	// Other media
	mux.HandleFunc("/tv", h.tvHome)
	mux.HandleFunc("/movies", h.moviesHome)

	// Static files
	mux.Handle(
		"/static/",
		http.StripPrefix("/static/", http.FileServer(http.Dir(h.staticDir))),
	)

	// Settings
	mux.HandleFunc("/settings", h.settings)
	mux.HandleFunc("/settings/jobs", h.settingsJobs)
	mux.HandleFunc("/settings/jobs.json", h.settingsJobsJSON)
	mux.HandleFunc("/settings/jobs/cancel", h.settingsJobsCancel)

	var handler http.Handler = mux
	if h.auth != nil {
		handler = h.auth.Middleware(handler)
	}
	return withLogging(handler)
}
