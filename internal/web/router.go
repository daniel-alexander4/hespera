package web

import (
	"net/http"
)

func (h *Handler) Router() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", h.home)
	mux.HandleFunc("/healthz", h.healthz)
	mux.HandleFunc("/shutdown", h.shutdown)
	mux.HandleFunc("/display/scale", h.displayScale)
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, h.staticFS, "hespera-favicon.ico")
	})

	// Libraries
	mux.HandleFunc("/libraries", h.libraries)
	mux.HandleFunc("/libraries/new", h.librariesNew)
	mux.HandleFunc("/libraries/media-root", h.librariesMediaRoot)
	mux.HandleFunc("/libraries/scan", h.librariesScan)
	mux.HandleFunc("/libraries/integrity-deep", h.librariesIntegrityDeep)
	mux.HandleFunc("/libraries/jobs-status", h.librariesJobsStatus)
	mux.HandleFunc("/libraries/delete", h.librariesDelete)

	// Music browse
	mux.HandleFunc("/music", h.musicHome)
	mux.HandleFunc("/music/artist/", h.musicArtistAlbums)
	mux.HandleFunc("/music/artist/external", h.musicArtistExternal)
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
	mux.HandleFunc("/tv/series/scan", h.tvSeriesScan)
	mux.HandleFunc("/tv/series/detect-intros", h.tvSeriesDetectIntros)
	mux.HandleFunc("/tv/series/", h.tvSeriesDetail)
	mux.HandleFunc("/tv/season/", h.tvSeasonDetail)
	mux.HandleFunc("/tv/match", h.tvMatch)
	mux.HandleFunc("/tv/match/review", h.tvMatchReview)
	mux.HandleFunc("/tv/match/approve", h.tvMatchApprove)
	mux.HandleFunc("/tv/match/skip", h.tvMatchSkip)
	mux.HandleFunc("/tv/match/rematch", h.tvMatchRematch)
	mux.HandleFunc("/tv/match/search", h.tvMatchSearch)
	mux.HandleFunc("/tv/player", h.tvPlayer)
	mux.HandleFunc("/person/", h.personDetail)
	mux.HandleFunc("/tv/playback-progress", h.tvPlaybackProgress)
	mux.HandleFunc("/tv/playback-session", h.tvPlaybackSession)
	mux.HandleFunc("/tv/subtitles/search", h.tvSubtitlesSearch)
	mux.HandleFunc("/tv/subtitles/fetch", h.tvSubtitlesFetch)
	mux.HandleFunc("/tv/subtitles/get", h.tvSubtitlesGet)

	// TV streaming
	mux.HandleFunc("/stream/tv/", h.streamTVEpisode)
	mux.HandleFunc("/stream/tv-remux/", h.streamTVRemux)
	mux.HandleFunc("/stream/tv-burnin/", h.streamTVBurnIn)
	mux.HandleFunc("/stream/tv-hls/", h.streamTVHLS)
	mux.HandleFunc("/stream/tv-subtitles/", h.streamTVSubtitles)

	// Movie playback + streaming (reuses the playback/video layers; thin clones
	// of the TV stream handlers over movie_files + movie_playback_progress).
	mux.HandleFunc("/movie/playback-session", h.moviePlaybackSession)
	mux.HandleFunc("/movie/playback-progress", h.moviePlaybackProgress)
	mux.HandleFunc("/stream/movie/", h.streamMovieDirect)
	mux.HandleFunc("/stream/movie-remux/", h.streamMovieRemux)
	mux.HandleFunc("/stream/movie-burnin/", h.streamMovieBurnIn)
	mux.HandleFunc("/stream/movie-hls/", h.streamMovieHLS)
	mux.HandleFunc("/stream/movie-subtitles/", h.streamMovieSubtitles)

	// TV art
	mux.HandleFunc("/art/tv/", h.tvArt)

	// Cast / actor images
	mux.HandleFunc("/art/person/", h.personArt)

	// Movie browse / detail / player / match
	mux.HandleFunc("/movies", h.moviesHome)
	mux.HandleFunc("/movie/player", h.moviePlayer)
	mux.HandleFunc("/movie/", h.movieDetail)
	mux.HandleFunc("/movies/match", h.moviesMatch)
	mux.HandleFunc("/movies/match/review", h.movieMatchReview)
	mux.HandleFunc("/movies/match/approve", h.moviesMatchApprove)
	mux.HandleFunc("/movies/match/skip", h.moviesMatchSkip)
	mux.HandleFunc("/movies/match/search", h.moviesMatchSearch)
	mux.HandleFunc("/movie/unmatch", h.movieUnmatch)
	mux.HandleFunc("/movie/art", h.movieArtUpload)
	mux.HandleFunc("/movie/art/clear", h.movieArtClear)
	mux.HandleFunc("/art/movie/", h.movieArt)

	// Static files (served from the embedded asset tree)
	mux.Handle(
		"/static/",
		http.StripPrefix("/static/", http.FileServer(http.FS(h.staticFS))),
	)

	// Settings
	mux.HandleFunc("/settings", h.settings)
	mux.HandleFunc("/settings/jobs", h.settingsJobs)
	mux.HandleFunc("/settings/jobs.json", h.settingsJobsJSON)
	mux.HandleFunc("/settings/jobs/fragment", h.settingsJobsFragment)
	mux.HandleFunc("/settings/jobs/cancel", h.settingsJobsCancel)
	mux.HandleFunc("/settings/tags", h.settingsTagEditor)
	mux.HandleFunc("/settings/api-keys", h.settingsAPIKeys)
	mux.HandleFunc("/settings/about", h.settingsAbout)
	mux.HandleFunc("/about/licenses", h.aboutLicenses)

	// Hespera has no authentication (a single-user media app). The CSRF guard —
	// which used to live in the auth middleware — wraps the mux unconditionally so
	// a cross-site page can't forge POSTs to the loopback/LAN port.
	handler := csrfGuard(mux)
	return withLogging(handler)
}
