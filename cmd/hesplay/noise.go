package main

// Ambient noise generation.
//
// This is a port of the brownnoise.sh that has been running on the upstairs Pi
// as a systemd unit (itself descended from a 2011 gist by Tom Swiss et al.).
// The shape of the sound is entirely SoX's: synthesize a short buffer of noise,
// band-pass it, modulate its volume very slowly so it breathes like surf, add a
// little reverb to smear the swell edges, then repeat the buffer. Pre-computing
// one buffer and repeating it is what keeps CPU near zero — the original's
// changelog claims 95%, and that property is preserved here.
//
// SoX rather than ffmpeg, deliberately: ffmpeg's `tremolo` filter has a
// MINIMUM frequency of 0.1 Hz, and the swell that makes this pleasant runs at
// 0.08 Hz. The one characteristic worth keeping is the one ffmpeg cannot
// express. ffmpeg also has no equivalent of SoX's `reverb`.
//
// Every parameter below is numeric or a whitelisted enum, and the argument
// vector is built directly (never a shell string), so no preset value can
// inject an argument however it was obtained.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// noiseTypes are SoX's synth waveforms for noise. The original script listed
// exactly these four; tpdf is Triangular Probability Density Function, the
// dither noise. White and pink carry more high frequency than brown.
var noiseTypes = []string{"whitenoise", "pinknoise", "brownnoise", "tpdfnoise"}

const (
	// noiseChannels matches the original's `-c 2`.
	noiseChannels = 2
	// noiseBuffer matches the original's `--buffer 131072` — 128 KB, which is
	// underrun protection on a small board.
	noiseBuffer = 131072

	// noiseMinBufferSecs is the floor for the synthesized buffer. The original
	// used exactly 60s; we round UP from here to a whole number of swells.
	noiseMinBufferSecs = 60.0

	// noiseSafetyCapSecs bounds `repeat` so a hesplay that dies without reaping
	// its child cannot leave the box roaring indefinitely. It is NOT how a
	// session's length is controlled — the caller cancels the context for that,
	// which kills the process. Matches the original's effective 24h ceiling.
	noiseSafetyCapSecs = 24 * 60 * 60

	// noiseMaxWaveSpeed is the original's own advice, verbatim: "increase for
	// more volume oscillation, but suggest no higher than 0.20".
	noiseMaxWaveSpeed = 0.20
)

// noisePreset is one named noise configuration. Field names avoid the word
// "amplitude" on purpose: in the original it could mean either the depth of the
// volume swell or the output level, and they are different knobs.
type noisePreset struct {
	Name string `json:"name"`
	Type string `json:"type"` // one of noiseTypes

	CenterHz float64 `json:"centerHz"` // band-pass centre
	WidthHz  float64 `json:"widthHz"`  // band-pass width

	WaveSpeed float64 `json:"waveSpeed"` // tremolo Hz — one swell per 1/speed seconds
	WaveDepth float64 `json:"waveDepth"` // tremolo % — how deep the swell goes

	Reverb float64 `json:"reverb"` // SoX reverberance %
	GainDB float64 `json:"gainDb"` // output level; 0 = leave the level alone
	FadeIn float64 `json:"fadeIn"` // seconds; 0 = start at full level
}

// noiseConfig is the whole on-disk file. It lives on the BOX that makes the
// sound, not on the Hespera server, so a scheduled window still fires when the
// music server is unreachable.
type noiseConfig struct {
	// AudioDev is the ALSA device, e.g. "plughw:0". Empty lets SoX choose,
	// which is right on a desktop; a board with several cards wants it pinned
	// (the systemd unit this replaces pinned plughw:0).
	AudioDev string `json:"audioDev,omitempty"`
	// Default names the preset used when a caller doesn't name one.
	Default string `json:"default,omitempty"`
	// Presets are the named configurations.
	Presets []noisePreset `json:"presets"`
	// Schedule is read by the reconciler. Declared here so the file shape is
	// stable from the first release.
	Schedule []noiseWindow `json:"schedule,omitempty"`
}

// noiseWindow is one scheduled span. Times are local "HH:MM"; a window whose
// end is not after its start WRAPS MIDNIGHT, which is the normal case here
// (20:00→10:00 is 14 hours across a day boundary). Days holds time.Weekday
// values; empty means every day.
//
// The reconciler that consumes this lives in schedule.go; the type is declared
// beside the rest of the config so the on-disk shape is settled in one place.
type noiseWindow struct {
	Start  string `json:"start"`            // "HH:MM", local time
	End    string `json:"end"`              // "HH:MM", local time
	Days   []int  `json:"days,omitempty"`   // time.Weekday; empty = every day
	Preset string `json:"preset,omitempty"` // empty = the config default
}

// defaultNoiseConfig reproduces the shipped brownnoise command as the "brown"
// preset. The other three colours deliberately reuse the same effect chain and
// differ only in waveform: the original tuned that chain by ear for brown, and
// inventing per-colour band centres nobody has listened to would be fabricating
// a decision. They are starting points to tune, not recommendations.
func defaultNoiseConfig() noiseConfig {
	base := func(name, typ string) noisePreset {
		return noisePreset{
			Name: name, Type: typ,
			CenterHz: 1786, WidthHz: 499,
			WaveSpeed: 0.08, WaveDepth: 37,
			Reverb: 19, GainDB: 0, FadeIn: 3,
		}
	}
	return noiseConfig{
		Default: "brown",
		Presets: []noisePreset{
			base("brown", "brownnoise"),
			base("pink", "pinknoise"),
			base("white", "whitenoise"),
			base("tpdf", "tpdfnoise"),
		},
	}
}

// noiseConfigPath sits beside the saved server file, under the same per-user
// config dir. Deliberately a SEPARATE file from hesplay-server: that one is
// read by every hesplay invocation including one-shot verbs, and this feature
// has no business touching that path.
func noiseConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "hespera", "hesplay.json"), nil
}

// loadNoiseConfig reads the config, falling back to the built-in defaults when
// there is no file yet. A file that exists but is corrupt is an error rather
// than a silent reset — silently replacing a tuned preset set would be worse
// than refusing to start.
func loadNoiseConfig() (noiseConfig, error) {
	p, err := noiseConfigPath()
	if err != nil {
		return defaultNoiseConfig(), nil
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return defaultNoiseConfig(), nil
	}
	if err != nil {
		return noiseConfig{}, err
	}
	var cfg noiseConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return noiseConfig{}, fmt.Errorf("%s: %w", p, err)
	}
	if len(cfg.Presets) == 0 {
		cfg.Presets = defaultNoiseConfig().Presets
	}
	return cfg, nil
}

// saveNoiseConfig writes the config atomically — temp file then rename — so an
// interrupted write cannot leave the box with a half-parsed schedule.
func saveNoiseConfig(cfg noiseConfig) error {
	p, err := noiseConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".hesplay-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename below succeeds
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, p)
}

// findPreset resolves a preset by name, case-insensitively, falling back to the
// configured default when the name is empty.
func (cfg noiseConfig) findPreset(name string) (noisePreset, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = cfg.Default
	}
	if name == "" && len(cfg.Presets) > 0 {
		return cfg.Presets[0], nil
	}
	for _, p := range cfg.Presets {
		if strings.EqualFold(p.Name, name) {
			return p, nil
		}
	}
	names := make([]string, 0, len(cfg.Presets))
	for _, p := range cfg.Presets {
		names = append(names, p.Name)
	}
	return noisePreset{}, fmt.Errorf("no noise preset %q (have: %s)", name, strings.Join(names, ", "))
}

// normalize clamps every field into a range SoX will accept and a human will
// tolerate, and fills a usable default for anything left at zero. It never
// returns an error for an out-of-range number: a preset edited to something
// silly should still make noise, just sane noise. An unknown TYPE is the one
// exception, because there is no sensible clamp for a waveform name.
func (p noisePreset) normalize() (noisePreset, error) {
	out := p
	out.Type = strings.ToLower(strings.TrimSpace(out.Type))
	if out.Type == "" {
		out.Type = "brownnoise"
	}
	// Accept the bare colour ("brown") as well as SoX's name ("brownnoise"),
	// since the original script's variable held the bare form.
	if !strings.HasSuffix(out.Type, "noise") {
		out.Type += "noise"
	}
	if !knownNoiseType(out.Type) {
		return noisePreset{}, fmt.Errorf("unknown noise type %q (have: %s)", p.Type, strings.Join(noiseTypes, ", "))
	}

	out.CenterHz = clampFloat(out.CenterHz, 20, 20000, 1786)
	out.WidthHz = clampFloat(out.WidthHz, 1, 20000, 499)
	out.WaveSpeed = clampFloat(out.WaveSpeed, 0.001, noiseMaxWaveSpeed, 0.08)
	out.WaveDepth = clampFloat(out.WaveDepth, 0, 100, 37)
	out.Reverb = clampFloat(out.Reverb, 0, 100, 19)
	out.FadeIn = clampFloat(out.FadeIn, 0, 300, 0)
	// Gain's zero value is meaningful (leave the level alone), so it is clamped
	// but never defaulted.
	out.GainDB = math.Max(-60, math.Min(20, out.GainDB))
	return out, nil
}

func knownNoiseType(t string) bool {
	for _, k := range noiseTypes {
		if k == t {
			return true
		}
	}
	return false
}

// clampFloat bounds v into [lo,hi], substituting def for a non-positive or
// non-finite value so a zeroed JSON field lands on something usable.
func clampFloat(v, lo, hi, def float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		v = def
	}
	return math.Max(lo, math.Min(hi, v))
}

// bufferSecs is the length of the synthesized buffer that gets repeated.
//
// It is DERIVED, never configured, and that is the whole point. The original
// hardcoded a 60s buffer while running the swell at 0.08 Hz — a 12.5s period —
// which is 4.8 swells per buffer, so the volume envelope jumped mid-swell at
// every loop point. Rounding up to a whole number of swells makes the loop
// seamless without changing the sound at all. A knob here could only ever be
// set to a value that seams.
func (p noisePreset) bufferSecs() float64 {
	period := 1 / p.WaveSpeed
	n := math.Ceil(noiseMinBufferSecs / period)
	if n < 1 {
		n = 1
	}
	return n * period
}

// repeatCount is the `repeat` argument: how many EXTRA times the buffer plays
// after the first. Sized to the safety cap, not to the session length — see
// noiseSafetyCapSecs.
func (p noisePreset) repeatCount() int {
	n := int(math.Ceil(noiseSafetyCapSecs/p.bufferSecs())) - 1
	if n < 0 {
		n = 0
	}
	return n
}

// noiseArgs builds the `play` argument vector.
//
// Effect ORDER is load-bearing. `repeat` replays everything before it in the
// chain, so `gain` and `fade` must come AFTER it: a fade placed before the
// repeat would re-fade on every single loop iteration, turning a one-time
// ease-in into a pulse every buffer length.
func (p noisePreset) noiseArgs() []string {
	a := []string{
		"--buffer", strconv.Itoa(noiseBuffer),
		"--no-show-progress",
		"-c", strconv.Itoa(noiseChannels),
		"--null",
		"synth", num(p.bufferSecs()), p.Type,
		"band", "-n", num(p.CenterHz), num(p.WidthHz),
		"tremolo", num(p.WaveSpeed), num(p.WaveDepth),
		"reverb", num(p.Reverb),
		"repeat", strconv.Itoa(p.repeatCount()),
	}
	if p.GainDB != 0 {
		a = append(a, "gain", num(p.GainDB))
	}
	if p.FadeIn > 0 {
		a = append(a, "fade", "t", num(p.FadeIn))
	}
	return a
}

// num formats a float the way a person would write it: no trailing zeros, no
// exponent. SoX parses these as plain seconds/Hz/percent.
func num(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// noisePlayer is the SoX playback binary. `play` is SoX's playback front-end;
// it is what the original script used and what both target boxes have.
type noisePlayer struct{ path string }

func findNoisePlayer() (noisePlayer, error) {
	p, err := exec.LookPath("play")
	if err != nil {
		return noisePlayer{}, errors.New("no noise engine: install sox (provides `play`)")
	}
	return noisePlayer{path: p}, nil
}

// noiseEnv is the environment for the play process. The original systemd unit
// set AUDIODRIVER=alsa and AUDIODEV=plughw:0 to pin the output; carrying that
// through config matters because SoX's default device need not be the one mpv
// picks, and a wrong-sink failure is silent.
func noiseEnv(audioDev string) []string {
	env := os.Environ()
	if d := strings.TrimSpace(audioDev); d != "" {
		env = append(env, "AUDIODRIVER=alsa", "AUDIODEV="+d)
	}
	return env
}

// runNoise plays one preset until the context is cancelled or the safety cap
// runs out. Cancelling kills the process — that is how a session ends, and why
// `repeat` is only a backstop rather than the timer.
//
// There is no stall guard here, unlike the music engine: SoX exposes no IPC
// socket, so a process that is alive but silent cannot be distinguished from
// one that is working. A process that EXITS is detectable and the caller
// restarts it; a wedged one is a known, accepted gap.
func runNoise(ctx context.Context, pl noisePlayer, p noisePreset, audioDev string) error {
	cmd := exec.CommandContext(ctx, pl.path, p.noiseArgs()...)
	cmd.Env = noiseEnv(audioDev)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil // cancelled: the intended way to stop
		}
		return fmt.Errorf("%s: %w", filepath.Base(pl.path), err)
	}
	return nil
}

// cmdNoise is the `noise` verb: play a preset, list them, or print the exact
// SoX invocation without running it.
//
//	hesplay noise                 play the default preset until Ctrl+C
//	hesplay noise brown           play a named preset
//	hesplay noise --for 45 brown  stop after 45 minutes
//	hesplay noise list            list the presets
//	hesplay noise --print brown   print the command, play nothing
//
// Arguments are scanned rather than passed to a FlagSet so a flag may sit
// anywhere, matching the rest of hesplay where a bare name needs no quoting.
func cmdNoise(ctx context.Context, args []string) error {
	var (
		printOnly bool
		list      bool
		minutes   float64
		words     []string
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case isHelp(a):
			usage()
			return nil
		case a == "--print" || a == "--print-command":
			printOnly = true
		case a == "list":
			list = true
		case a == "--for" || a == "-for":
			if i+1 >= len(args) {
				return errors.New("--for: expected a number of minutes")
			}
			i++
			m, err := strconv.ParseFloat(args[i], 64)
			if err != nil || m <= 0 {
				return fmt.Errorf("--for: %q is not a positive number of minutes", args[i])
			}
			minutes = m
		case strings.HasPrefix(a, "--for="):
			m, err := strconv.ParseFloat(strings.TrimPrefix(a, "--for="), 64)
			if err != nil || m <= 0 {
				return fmt.Errorf("--for: %q is not a positive number of minutes", a)
			}
			minutes = m
		default:
			words = append(words, a)
		}
	}

	cfg, err := loadNoiseConfig()
	if err != nil {
		return err
	}
	if list {
		return listNoisePresets(cfg)
	}

	preset, err := cfg.findPreset(strings.Join(words, " "))
	if err != nil {
		return err
	}
	preset, err = preset.normalize()
	if err != nil {
		return err
	}

	pl, err := findNoisePlayer()
	if err != nil {
		return err
	}
	if printOnly {
		fmt.Println(noiseCommandLine(pl, preset, cfg.AudioDev))
		return nil
	}

	if minutes > 0 {
		fmt.Printf("%s noise for %g minutes (Ctrl+C to stop)\n", preset.Name, minutes)
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(minutes*float64(time.Minute)))
		defer cancel()
	} else {
		fmt.Printf("%s noise (Ctrl+C to stop)\n", preset.Name)
	}
	return runNoise(ctx, pl, preset, cfg.AudioDev)
}

// listNoisePresets prints the configured presets, marking the default. The
// swell period is shown derived rather than raw because "one swell every 12.5s"
// is the thing a person is actually choosing.
func listNoisePresets(cfg noiseConfig) error {
	tw := newTable("NAME", "TYPE", "BAND", "SWELL", "DEPTH", "REVERB", "GAIN")
	for _, p := range cfg.Presets {
		n, err := p.normalize()
		if err != nil {
			return err
		}
		name := n.Name
		if strings.EqualFold(name, cfg.Default) {
			name += " *"
		}
		fmt.Fprintf(tw, "%s\t%s\t%g±%g Hz\t%.1fs\t%g%%\t%g%%\t%+g dB\n",
			name, strings.TrimSuffix(n.Type, "noise"),
			n.CenterHz, n.WidthHz, 1/n.WaveSpeed, n.WaveDepth, n.Reverb, n.GainDB)
	}
	return tw.Flush()
}

// noiseCommandLine renders the invocation for --print-command and for tests.
// Quoting is display-only: the real call passes the vector to exec, so nothing
// here is ever parsed by a shell.
func noiseCommandLine(pl noisePlayer, p noisePreset, audioDev string) string {
	var b strings.Builder
	if d := strings.TrimSpace(audioDev); d != "" {
		b.WriteString("AUDIODRIVER=alsa AUDIODEV=" + d + " ")
	}
	b.WriteString(filepath.Base(pl.path))
	for _, a := range p.noiseArgs() {
		b.WriteString(" ")
		if strings.ContainsAny(a, " \t") {
			b.WriteString(strconv.Quote(a))
			continue
		}
		b.WriteString(a)
	}
	return b.String()
}
