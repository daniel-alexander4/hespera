package web

import (
	"context"
	"fmt"
	"html/template"
)

// Subtitle appearance settings: three enums stored in app_settings, mapped here
// to fixed CSS custom-property values the player templates inline on
// .tv-player-video-wrap (style="--cap-*: …"). The caption CSS reads them via
// var() with the current look as fallback, so absent/default settings render
// exactly as before. Values are whitelisted Go literals — read-time validated
// (enumOr), so an unvalidated hescli write degrades to the default and nothing
// user-controlled ever reaches the template.CSS. Applies to self-rendered text
// sidecars only; bitmap (burn-in) subtitles are pixels in the video stream.
var (
	capScale = map[string]string{"small": "0.85", "normal": "1", "large": "1.25", "xlarge": "1.5"}
	capBG    = map[string]string{"none": "rgba(0,0,0,0)", "translucent": "rgba(0,0,0,0.6)", "solid": "rgba(0,0,0,0.9)"}
	capRaise = map[string]string{"normal": "0rem", "raised": "2.5rem", "high": "5rem"}
)

// enumOr returns v when it is a key of allowed, else def.
func enumOr(v, def string, allowed map[string]string) string {
	if _, ok := allowed[v]; ok {
		return v
	}
	return def
}

// effectiveSubtitleSize/Bg/Position return the stored appearance enum,
// read-time validated against the whitelist (invalid/absent → default).
func (h *Handler) effectiveSubtitleSize(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='subtitle_size'").Scan(&v)
	return enumOr(v, "normal", capScale)
}

func (h *Handler) effectiveSubtitleBg(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='subtitle_bg'").Scan(&v)
	return enumOr(v, "translucent", capBG)
}

func (h *Handler) effectiveSubtitlePosition(ctx context.Context) string {
	var v string
	_ = h.db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key='subtitle_position'").Scan(&v)
	return enumOr(v, "normal", capRaise)
}

// captionStyleVars resolves the three appearance enums to the inline CSS
// custom-property declarations for the player wrapper. template.CSS is safe
// here because every value is a literal from the maps above, never user input.
func (h *Handler) captionStyleVars(ctx context.Context) template.CSS {
	return template.CSS(fmt.Sprintf("--cap-scale:%s;--cap-bg:%s;--cap-raise:%s",
		capScale[h.effectiveSubtitleSize(ctx)],
		capBG[h.effectiveSubtitleBg(ctx)],
		capRaise[h.effectiveSubtitlePosition(ctx)]))
}
