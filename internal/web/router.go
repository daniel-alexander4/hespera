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
	mux.HandleFunc("/music/artist/disambiguate", h.musicArtistDisambiguate)
	mux.HandleFunc("/music/artist/art", h.musicArtistArt)
	mux.HandleFunc("/music/albums", h.musicAlbums)
	mux.HandleFunc("/music/album/", h.musicAlbumTracks)
	mux.HandleFunc("/music/album/edit", h.musicAlbumEdit)
	mux.HandleFunc("/music/track/edit", h.musicTrackEdit)
	mux.HandleFunc("/music/album/art", h.musicAlbumArtUpload)
	mux.HandleFunc("/music/album/art/clear", h.musicAlbumArtClear)
	mux.HandleFunc("/music/album/unmatch", h.musicAlbumUnmatch)
	mux.HandleFunc("/music/album/reassign", h.musicAlbumReassign)
	mux.HandleFunc("/music/album/rescan", h.musicAlbumRescan)
	mux.HandleFunc("/music/compilations", h.musicCompilations)
	mux.HandleFunc("/music/player", h.musicPlayer)
	mux.HandleFunc("/music/queue", h.musicQueue)
	mux.HandleFunc("/music/play-event", h.musicPlayEvent)
	mux.HandleFunc("/music/lyrics/fetch", h.musicLyricsFetch)

	// Streaming
	mux.HandleFunc("/stream/track/", h.streamTrack)

	// Art
	mux.HandleFunc("/art/album/", h.albumArt)
	mux.HandleFunc("/art/artist/", h.artistArt)

	// Music matching
	mux.HandleFunc("/music/match", h.musicMatch)
	mux.HandleFunc("/music/match/review", h.musicMatchReview)
	mux.HandleFunc("/music/match/approve", h.musicMatchApprove)
	mux.HandleFunc("/music/match/approve-all", h.musicMatchApproveAll)
	mux.HandleFunc("/music/match/reject", h.musicMatchReject)
	mux.HandleFunc("/music/match/rematch", h.musicMatchRematch)
	mux.HandleFunc("/music/writeback", h.musicWriteback)
	mux.HandleFunc("/music/duplicates", h.musicDuplicates)
	mux.HandleFunc("/music/duplicates/merge", h.musicDuplicatesMerge)

	// TV browse
	mux.HandleFunc("/tv", h.tvSeriesList)
	mux.HandleFunc("/tv/series/", h.tvSeriesDetail)
	mux.HandleFunc("/tv/season/", h.tvSeasonDetail)
	mux.HandleFunc("/tv/match", h.tvMatch)
	mux.HandleFunc("/tv/match/review", h.tvMatchReview)
	mux.HandleFunc("/tv/match/approve", h.tvMatchApprove)
	mux.HandleFunc("/tv/match/skip", h.tvMatchSkip)
	mux.HandleFunc("/tv/match/rematch", h.tvMatchRematch)
	mux.HandleFunc("/tv/match/search", h.tvMatchSearch)
	mux.HandleFunc("/tv/player", h.tvPlayer)
	mux.HandleFunc("/tv/playback-progress", h.tvPlaybackProgress)
	mux.HandleFunc("/tv/playback-session", h.tvPlaybackSession)

	// TV streaming
	mux.HandleFunc("/stream/tv/", h.streamTVEpisode)
	mux.HandleFunc("/stream/tv-remux/", h.streamTVRemux)
	mux.HandleFunc("/stream/tv-hls/", h.streamTVHLS)
	mux.HandleFunc("/stream/tv-subtitles/", h.streamTVSubtitles)

	// TV art
	mux.HandleFunc("/art/tv/", h.tvArt)

	// Other media
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
	mux.HandleFunc("/settings/jobs/fragment", h.settingsJobsFragment)
	mux.HandleFunc("/settings/jobs/cancel", h.settingsJobsCancel)
	mux.HandleFunc("/settings/tags", h.settingsTagEditor)
	mux.HandleFunc("/settings/api-keys", h.settingsAPIKeys)

	var handler http.Handler = mux
	if h.auth != nil {
		handler = h.auth.Middleware(handler)
	}
	return withLogging(handler)
}
