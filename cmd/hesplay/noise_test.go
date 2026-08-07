package main

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// originalScriptArgs is the command that has actually been running on the
// upstairs Pi, verbatim from brownnoise.sh with its defaults substituted
// (minutes=1440 → repeat 1439, center=1786, wave=0.08). It is the reference the
// port must not drift from by accident.
var originalScriptArgs = []string{
	"--buffer", "131072", "--no-show-progress", "-c", "2", "--null",
	"synth", "01:00", "brownnoise",
	"band", "-n", "1786", "499",
	"tremolo", "0.08", "37",
	"reverb", "19",
	"repeat", "1439",
}

// TestNoiseArgsMatchOriginalExceptIntendedChanges pins the port against the
// script. Three differences are deliberate and everything else must match:
// the buffer becomes a whole number of swells (the loop-seam fix), the repeat
// count is recomputed for that buffer, and a fade-in is appended.
func TestNoiseArgsMatchOriginalExceptIntendedChanges(t *testing.T) {
	p, err := defaultNoiseConfig().Presets[0].normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.Name != "brown" {
		t.Fatalf("expected the first default preset to be brown, got %q", p.Name)
	}
	got := p.noiseArgs()

	// Compare position by position up to `synth`'s length argument, which is
	// the first intended difference.
	for i := 0; i < 7; i++ {
		if got[i] != originalScriptArgs[i] {
			t.Errorf("arg %d: got %q, original %q", i, got[i], originalScriptArgs[i])
		}
	}
	// The waveform and the whole effect chain up to `repeat` must be identical.
	gotChain := strings.Join(got[8:19], " ")
	wantChain := strings.Join(originalScriptArgs[8:19], " ")
	if gotChain != wantChain {
		t.Errorf("effect chain drifted:\n got %q\nwant %q", gotChain, wantChain)
	}
	// And the intended differences are present.
	if got[7] == "01:00" {
		t.Error("buffer length was not changed: the loop seam fix is missing")
	}
	if !strings.Contains(strings.Join(got, " "), "fade t 3") {
		t.Errorf("fade-in missing from %v", got)
	}
}

// TestNoiseBufferIsWholeSwells is the loop-seam fix itself. The original ran a
// 12.5s swell inside a 60s buffer — 4.8 swells — so the volume envelope jumped
// mid-swell at every loop point. The buffer must be a whole number of swells
// and still at least the 60s floor.
func TestNoiseBufferIsWholeSwells(t *testing.T) {
	for _, speed := range []float64{0.08, 0.1, 0.2, 0.05, 0.033, 0.011} {
		p := noisePreset{Type: "brownnoise", WaveSpeed: speed}
		p, err := p.normalize()
		if err != nil {
			t.Fatalf("normalize %g: %v", speed, err)
		}
		buf := p.bufferSecs()
		period := 1 / p.WaveSpeed
		swells := buf / period
		if math.Abs(swells-math.Round(swells)) > 1e-9 {
			t.Errorf("speed %g: buffer %gs is %g swells of %gs — not a whole number", speed, buf, swells, period)
		}
		if buf < noiseMinBufferSecs {
			t.Errorf("speed %g: buffer %gs is below the %gs floor", speed, buf, noiseMinBufferSecs)
		}
		// One swell shorter would drop under the floor — i.e. it rounded UP by
		// the minimum needed, rather than overshooting.
		if buf-period >= noiseMinBufferSecs {
			t.Errorf("speed %g: buffer %gs overshoots; %gs would still clear the floor", speed, buf, buf-period)
		}
	}
}

// TestNoiseGainAndFadeFollowRepeat guards the one ordering mistake that is easy
// to make and hard to hear as a bug: `repeat` replays everything before it, so
// a fade placed ahead of it becomes a pulse once per buffer instead of a
// one-time ease-in.
func TestNoiseGainAndFadeFollowRepeat(t *testing.T) {
	p, err := noisePreset{Type: "brown", WaveSpeed: 0.08, GainDB: -3, FadeIn: 5}.normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	args := p.noiseArgs()
	idx := func(s string) int {
		for i, a := range args {
			if a == s {
				return i
			}
		}
		return -1
	}
	rep, gain, fade := idx("repeat"), idx("gain"), idx("fade")
	if rep < 0 || gain < 0 || fade < 0 {
		t.Fatalf("expected repeat, gain and fade in %v", args)
	}
	if gain < rep {
		t.Errorf("gain at %d precedes repeat at %d: it would re-apply per loop", gain, rep)
	}
	if fade < rep {
		t.Errorf("fade at %d precedes repeat at %d: it would pulse every buffer", fade, rep)
	}
}

// TestNoiseGainOmittedWhenZero: zero gain means "leave the level alone", so the
// effect must not appear at all — a `gain 0` would be a no-op but it would also
// mean the shipped brown preset no longer matches the original chain.
func TestNoiseGainOmittedWhenZero(t *testing.T) {
	p, _ := noisePreset{Type: "brown", WaveSpeed: 0.08}.normalize()
	for _, a := range p.noiseArgs() {
		if a == "gain" {
			t.Fatalf("gain present with GainDB==0: %v", p.noiseArgs())
		}
	}
}

func TestNoisePresetNormalize(t *testing.T) {
	t.Run("bare colour accepted", func(t *testing.T) {
		for _, in := range []string{"brown", "brownnoise", "BROWN", " Pink "} {
			p, err := noisePreset{Type: in}.normalize()
			if err != nil {
				t.Fatalf("%q: %v", in, err)
			}
			if !strings.HasSuffix(p.Type, "noise") {
				t.Errorf("%q normalized to %q", in, p.Type)
			}
		}
	})

	t.Run("unknown type is an error", func(t *testing.T) {
		if _, err := (noisePreset{Type: "greennoise"}).normalize(); err == nil {
			t.Fatal("expected an error for an unknown waveform")
		}
	})

	t.Run("wave speed clamped to the script's advice", func(t *testing.T) {
		p, err := noisePreset{Type: "brown", WaveSpeed: 5}.normalize()
		if err != nil {
			t.Fatal(err)
		}
		if p.WaveSpeed != noiseMaxWaveSpeed {
			t.Errorf("WaveSpeed 5 should clamp to %g, got %g", noiseMaxWaveSpeed, p.WaveSpeed)
		}
	})

	t.Run("zero fields fall back to the original's values", func(t *testing.T) {
		p, err := noisePreset{Type: "brown"}.normalize()
		if err != nil {
			t.Fatal(err)
		}
		if p.CenterHz != 1786 || p.WidthHz != 499 || p.WaveSpeed != 0.08 || p.WaveDepth != 37 || p.Reverb != 19 {
			t.Errorf("defaults drifted from the original script: %+v", p)
		}
	})

	t.Run("gain is clamped but never defaulted", func(t *testing.T) {
		p, _ := noisePreset{Type: "brown", GainDB: 0}.normalize()
		if p.GainDB != 0 {
			t.Errorf("zero gain must stay zero, got %g", p.GainDB)
		}
		if p, _ := (noisePreset{Type: "brown", GainDB: 999}).normalize(); p.GainDB != 20 {
			t.Errorf("gain 999 should clamp to 20, got %g", p.GainDB)
		}
		if p, _ := (noisePreset{Type: "brown", GainDB: -999}).normalize(); p.GainDB != -60 {
			t.Errorf("gain -999 should clamp to -60, got %g", p.GainDB)
		}
	})

	t.Run("NaN and Inf fall back rather than reaching the command", func(t *testing.T) {
		p, err := noisePreset{Type: "brown", CenterHz: math.NaN(), WaveSpeed: math.Inf(1)}.normalize()
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range p.noiseArgs() {
			if strings.Contains(a, "NaN") || strings.Contains(a, "Inf") {
				t.Fatalf("non-finite value reached the argument vector: %v", p.noiseArgs())
			}
		}
	})
}

// TestNoiseArgsAreAllNumericOrKeywords is the injection guard, stated as a
// property rather than as escaping: every generated argument is either a known
// SoX keyword or a plain number, so no preset value can become an argument of
// its own however it got into the config.
func TestNoiseArgsAreAllNumericOrKeywords(t *testing.T) {
	keywords := map[string]bool{
		"--buffer": true, "--no-show-progress": true, "-c": true, "--null": true,
		"synth": true, "band": true, "-n": true, "tremolo": true, "reverb": true,
		"repeat": true, "gain": true, "fade": true, "t": true,
	}
	for _, typ := range noiseTypes {
		keywords[typ] = true
	}
	p, err := noisePreset{
		Type: "brown", CenterHz: 1786, WidthHz: 499,
		WaveSpeed: 0.08, WaveDepth: 37, Reverb: 19, GainDB: -3, FadeIn: 5,
	}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range p.noiseArgs() {
		if keywords[a] {
			continue
		}
		if strings.ContainsAny(a, " ;|&$`'\"\\\n") {
			t.Fatalf("argument %q is neither a keyword nor a plain value", a)
		}
		if _, err := strconv.ParseFloat(a, 64); err != nil {
			t.Fatalf("argument %q is neither a keyword nor numeric", a)
		}
	}
}

func TestFindPreset(t *testing.T) {
	cfg := defaultNoiseConfig()

	t.Run("empty name uses the default", func(t *testing.T) {
		p, err := cfg.findPreset("")
		if err != nil || p.Name != "brown" {
			t.Fatalf("got %q, %v; want brown", p.Name, err)
		}
	})
	t.Run("case insensitive", func(t *testing.T) {
		if p, err := cfg.findPreset("PINK"); err != nil || p.Name != "pink" {
			t.Fatalf("got %q, %v; want pink", p.Name, err)
		}
	})
	t.Run("unknown name lists what exists", func(t *testing.T) {
		_, err := cfg.findPreset("purple")
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), "brown") {
			t.Errorf("error should list the available presets, got %q", err)
		}
	})
}

func TestNoiseConfigRoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// No file yet → the built-in defaults, not an error.
	cfg, err := loadNoiseConfig()
	if err != nil {
		t.Fatalf("load with no file: %v", err)
	}
	// Asserted as a SET rather than a count: the count is incidental and grew
	// when the ffmpeg-generated colours were added, but every one of these names
	// is something a saved schedule or a phone tile may already refer to.
	have := map[string]bool{}
	for _, p := range cfg.Presets {
		have[p.Name] = true
	}
	for _, want := range []string{"brown", "pink", "white", "tpdf", "blue", "violet", "velvet"} {
		if !have[want] {
			t.Errorf("built-in preset %q missing (have %v)", want, have)
		}
	}

	cfg.AudioDev = "plughw:0"
	cfg.Schedule = []noiseWindow{{Start: "20:00", End: "10:00", Days: []int{1, 2}, Preset: "brown"}}
	cfg.Presets[0].WaveSpeed = 0.12
	if err := saveNoiseConfig(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := loadNoiseConfig()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.AudioDev != "plughw:0" {
		t.Errorf("AudioDev round trip: got %q", got.AudioDev)
	}
	if len(got.Schedule) != 1 || got.Schedule[0].Start != "20:00" || got.Schedule[0].End != "10:00" {
		t.Errorf("schedule round trip: got %+v", got.Schedule)
	}
	if got.Presets[0].WaveSpeed != 0.12 {
		t.Errorf("preset round trip: got %g", got.Presets[0].WaveSpeed)
	}

	// The file is written where it is documented to be.
	p, err := noiseConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(p) != "hesplay.json" {
		t.Errorf("config filename: got %q", filepath.Base(p))
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("config not at %s: %v", p, err)
	}
}

// TestLoadNoiseConfigRejectsCorruptFile: silently replacing a tuned preset set
// with the defaults would be worse than refusing to start.
func TestLoadNoiseConfigRejectsCorruptFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	p, err := noiseConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadNoiseConfig(); err == nil {
		t.Fatal("expected an error for a corrupt config, not a silent reset")
	}
}

// TestNoiseEnvPinsDeviceOnlyWhenAsked: the systemd unit this replaces pinned
// AUDIODEV=plughw:0, and SoX's default sink need not be the one mpv picks — but
// pinning a device that doesn't exist on a laptop would be worse than not
// pinning at all, so it is opt-in.
func TestNoiseEnvPinsDeviceOnlyWhenAsked(t *testing.T) {
	has := func(env []string, k string) bool {
		for _, e := range env {
			if strings.HasPrefix(e, k+"=") {
				return true
			}
		}
		return false
	}
	if env := noiseEnv(""); has(env, "AUDIODEV") {
		t.Error("empty audioDev must not pin a device")
	}
	env := noiseEnv("plughw:0")
	if !has(env, "AUDIODEV") || !has(env, "AUDIODRIVER") {
		t.Errorf("audioDev should set both AUDIODRIVER and AUDIODEV, got %v", env[len(env)-2:])
	}
}

// --- hybrid colours (ffmpeg generates, SoX shapes) ------------------------

// TestNoiseNeedsFFmpegOnlyForColoursSoxLacks pins the split. SoX genuinely
// cannot make these — `sox -n synth 1 bluenoise` is rejected and its manpage
// lists only the four synth waveforms — so routing one of the native four
// through ffmpeg would be a pointless second process, and routing blue through
// SoX would simply fail at play time.
func TestNoiseNeedsFFmpegOnlyForColoursSoxLacks(t *testing.T) {
	native := []string{"whitenoise", "pinknoise", "brownnoise", "tpdfnoise"}
	hybrid := []string{"bluenoise", "violetnoise", "velvetnoise"}

	for _, typ := range native {
		p, err := noisePreset{Type: typ}.normalize()
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if p.noiseNeedsFFmpeg() {
			t.Errorf("%s is a SoX synth waveform and must not need ffmpeg", typ)
		}
	}
	for _, typ := range hybrid {
		p, err := noisePreset{Type: typ}.normalize()
		if err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		if !p.noiseNeedsFFmpeg() {
			t.Errorf("%s does not exist in SoX and must be generated by ffmpeg", typ)
		}
		if ffmpegColors[p.Type] == "" {
			t.Errorf("%s has no anoisesrc colour mapped", typ)
		}
	}
}

// TestHybridAndNativeShareTheEffectChain is the whole point of the hybrid: the
// band-pass, swell and reverb that were tuned by ear for brown must apply
// identically to a colour ffmpeg generated. If these ever diverge, a hybrid
// colour stops being the same instrument.
func TestHybridAndNativeShareTheEffectChain(t *testing.T) {
	mk := func(typ string) noisePreset {
		p, err := noisePreset{Type: typ, WaveSpeed: 0.08, WaveDepth: 37, CenterHz: 1786, WidthHz: 499, Reverb: 19}.normalize()
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	native, hybrid := mk("brownnoise"), mk("bluenoise")
	if got, want := strings.Join(hybrid.shapeArgs(), " "), strings.Join(native.shapeArgs(), " "); got != want {
		t.Fatalf("effect chains differ:\n hybrid %q\n native %q", got, want)
	}
}

// TestHybridSoxArgsHaveNoSynthOrRepeat: the stream is endless and generated
// elsewhere. `synth` would be a second source, and `repeat` buffers its input
// to replay it — on an endless pipe that is an unbounded buffer, not a loop.
func TestHybridSoxArgsHaveNoSynthOrRepeat(t *testing.T) {
	p, err := noisePreset{Type: "blue", WaveSpeed: 0.08}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	args := p.soxPipeArgs()
	joined := strings.Join(args, " ")
	for _, banned := range []string{"synth", "repeat"} {
		for _, a := range args {
			if a == banned {
				t.Errorf("%q must not appear on the pipe path: %s", banned, joined)
			}
		}
	}
	if !strings.Contains(joined, "-t wav -") {
		t.Errorf("pipe path must read a wav stream from stdin, got %s", joined)
	}
	// -c 2 describes the null INPUT device on the native path; here the channel
	// count comes from the piped WAV header, and forcing it would be an override.
	for i, a := range args {
		if a == "-c" {
			t.Errorf("-c must not appear on the pipe path (arg %d): %s", i, joined)
		}
	}
	// noiseArgs must route to the pipe form for a hybrid colour.
	if strings.Join(p.noiseArgs(), " ") != joined {
		t.Error("noiseArgs did not route a hybrid colour to soxPipeArgs")
	}
}

// TestHybridFFmpegArgsAreBoundedAndStereo: the generator needs the same 24h
// ceiling `repeat` gives the native path, and must match the native path's
// channel count.
func TestHybridFFmpegArgsAreBoundedAndStereo(t *testing.T) {
	p, err := noisePreset{Type: "violet"}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(p.ffmpegArgs(), " ")
	if !strings.Contains(joined, "anoisesrc=color=violet") {
		t.Errorf("wrong generator source: %s", joined)
	}
	if !strings.Contains(joined, ":d="+strconv.Itoa(noiseSafetyCapSecs)) {
		t.Errorf("generator is unbounded — a hesplay that dies would leave it running: %s", joined)
	}
	if !strings.Contains(joined, "-ac "+strconv.Itoa(noiseChannels)) {
		t.Errorf("anoisesrc is mono; the native path plays %d channels: %s", noiseChannels, joined)
	}
}

// TestNoiseCommandLineRendersPipelineForHybrid — --print exists so the exact
// invocation can be pasted into a shell; for a hybrid colour that means both
// halves and the pipe.
func TestNoiseCommandLineRendersPipelineForHybrid(t *testing.T) {
	pl := noisePlayer{path: "/usr/bin/play"}

	native, err := noisePreset{Type: "brown"}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got := noiseCommandLine(pl, native, ""); strings.Contains(got, "|") {
		t.Errorf("a SoX-native colour must render as one command, got %q", got)
	}

	hybrid, err := noisePreset{Type: "blue"}.normalize()
	if err != nil {
		t.Fatal(err)
	}
	got := noiseCommandLine(pl, hybrid, "plughw:0")
	if !strings.Contains(got, " | ") {
		t.Errorf("a hybrid colour must render as a pipeline, got %q", got)
	}
	if !strings.Contains(got, "anoisesrc") || !strings.Contains(got, "play ") {
		t.Errorf("pipeline is missing a half: %q", got)
	}
	// The device pin belongs to the player half, not the generator half —
	// ffmpeg never touches the sound card here.
	pipeAt := strings.Index(got, " | ")
	if strings.Contains(got[:pipeAt], "AUDIODEV") {
		t.Errorf("AUDIODEV must sit with the player, not the generator: %q", got)
	}
	if !strings.Contains(got[pipeAt:], "AUDIODEV=plughw:0") {
		t.Errorf("AUDIODEV missing from the player half: %q", got)
	}
}

// TestDefaultPresetLevelsAreMatched: the colours land up to 20 dB apart through
// the same band-pass, so shipping them all at gain 0 would make switching to
// violet sound like the noise had stopped. Brown stays at 0 — it is the
// reference, and its command must remain the original's.
func TestDefaultPresetLevelsAreMatched(t *testing.T) {
	byName := map[string]noisePreset{}
	for _, p := range defaultNoiseConfig().Presets {
		byName[p.Name] = p
	}
	if got := byName["brown"].GainDB; got != 0 {
		t.Errorf("brown is the reference and must carry no gain, got %g", got)
	}
	for _, name := range []string{"pink", "white", "tpdf", "blue", "violet"} {
		p, ok := byName[name]
		if !ok {
			t.Fatalf("preset %q missing from the defaults", name)
		}
		if p.GainDB <= 0 {
			t.Errorf("%s measured quieter than brown through the same chain and needs a positive gain, got %g", name, p.GainDB)
		}
	}
	// Every default must survive normalization — a shipped preset that clamps
	// to something else would mean the measured values never reach SoX.
	for name, p := range byName {
		n, err := p.normalize()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if n.GainDB != p.GainDB {
			t.Errorf("%s gain %g was clamped to %g", name, p.GainDB, n.GainDB)
		}
	}
}

// TestOceanChainedArgs pins the two-swell single-stream flavour: the slow
// tremolo chains directly after the fast one (envelopes multiply — the tide
// under the waves), and the buffer grows to 100s — the shortest length holding
// a whole number of BOTH periods (8 × 12.5s and 2 × 50s), so the loop stays
// seamless for both envelopes.
func TestOceanChainedArgs(t *testing.T) {
	p, err := (noisePreset{Type: "brownnoise", CenterHz: 1786, WidthHz: 499,
		WaveSpeed: 0.08, WaveDepth: 37, Wave2Speed: 0.02, Wave2Depth: 60,
		Reverb: 19, FadeIn: 3}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []string{
		"--buffer", "131072", "--no-show-progress", "-c", "2", "--null",
		"synth", "100", "brownnoise",
		"band", "-n", "1786", "499",
		"tremolo", "0.08", "37",
		"tremolo", "0.02", "60",
		"reverb", "19",
		"repeat", "863",
		"fade", "t", "3",
	}
	if got := p.noiseArgs(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ocean argv:\n got %q\nwant %q", strings.Join(got, " "), strings.Join(want, " "))
	}
}

// TestOceanPinkLayeredArgs pins the two-colour flavour: the pink layer
// synthesizes FIRST so its gain trim and slow tremolo envelope it alone, the
// brown bed mixes in un-swelled, and the shared shaping (band, fast tremolo,
// reverb) applies to the sum. The order is load-bearing — a tremolo placed
// after the mix would swell both layers together and the bed would vanish at
// every trough.
func TestOceanPinkLayeredArgs(t *testing.T) {
	p, err := (noisePreset{Type: "brownnoise", CenterHz: 1786, WidthHz: 499,
		WaveSpeed: 0.08, WaveDepth: 37,
		Wave2Type: "pinknoise", Wave2Gain: 7, Wave2Speed: 0.02, Wave2Depth: 60,
		Reverb: 19, FadeIn: 3}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	want := []string{
		"--buffer", "131072", "--no-show-progress", "-c", "2", "--null",
		"synth", "100", "pinknoise",
		"gain", "7",
		"tremolo", "0.02", "60",
		"synth", "brownnoise", "mix",
		"band", "-n", "1786", "499",
		"tremolo", "0.08", "37",
		"reverb", "19",
		"repeat", "863",
		"fade", "t", "3",
	}
	if got := p.noiseArgs(); strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("ocean pink argv:\n got %q\nwant %q", strings.Join(got, " "), strings.Join(want, " "))
	}
}

func mustFindDefault(t *testing.T, name string) noisePreset {
	t.Helper()
	p, err := defaultNoiseConfig().findPreset(name)
	if err != nil {
		t.Fatalf("default preset %q: %v", name, err)
	}
	return p
}

// TestCommensurateBufferSecs covers the two-period derivation: exact common
// periods where they exist under the cap, the seam-the-fast-swell fallback
// where they don't, and the whole-fast-periods inversion when the slow period
// alone exceeds the cap.
func TestCommensurateBufferSecs(t *testing.T) {
	cases := []struct {
		p1, p2, want float64
	}{
		{12.5, 50, 100},                  // the ocean defaults: 8 and 2 whole swells
		{12.5, 100, 100},                 // slow period ≥ floor and already commensurate
		{10, 1.0 / 0.03, 100},            // 3 × 33.33s ≈ 10 × 10s
		{12.5, 1.0 / 0.007, 1.0 / 0.007}, // incommensurate → whole slow periods only
		{12.5, 500, 62.5},                // slow period past the cap → whole fast periods
	}
	for _, c := range cases {
		got := commensurateBufferSecs(c.p1, c.p2)
		if math.Abs(got-c.want) > 1e-6 {
			t.Errorf("commensurateBufferSecs(%g, %g) = %g, want %g", c.p1, c.p2, got, c.want)
		}
		if got > noiseMaxBufferSecs+1e-9 {
			t.Errorf("commensurateBufferSecs(%g, %g) = %g exceeds the %g cap", c.p1, c.p2, got, noiseMaxBufferSecs)
		}
	}
}

// TestNormalizeWave2 pins the second-swell validation: bare colour names get
// the noise suffix, ffmpeg-only colours are structurally impossible on either
// side of a layered chain, a layer defaults its swell on, and a negative rate
// switches the feature off rather than clamping it on.
func TestNormalizeWave2(t *testing.T) {
	if _, err := (noisePreset{Type: "brown", Wave2Type: "blue"}).normalize(); err == nil {
		t.Error("an ffmpeg-only second-layer colour must be rejected")
	}
	if _, err := (noisePreset{Type: "violet", Wave2Type: "pink"}).normalize(); err == nil {
		t.Error("a layered preset with an ffmpeg-only base colour must be rejected")
	}
	p, err := (noisePreset{Type: "brown", Wave2Type: "pink"}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.Wave2Type != "pinknoise" {
		t.Errorf("bare layer colour not suffixed: %q", p.Wave2Type)
	}
	if p.Wave2Speed != 0.02 || p.Wave2Depth != 60 {
		t.Errorf("a layer without a swell should default one: got %g/%g", p.Wave2Speed, p.Wave2Depth)
	}
	p, err = (noisePreset{Type: "brown", WaveSpeed: 0.08, Wave2Speed: -1, Wave2Depth: 40}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.Wave2Speed != 0 || p.Wave2Depth != 0 {
		t.Errorf("a negative second swell should switch off, got %g/%g", p.Wave2Speed, p.Wave2Depth)
	}
}

// TestOceanBrownStages pins the shipped three-layer preset's process topology:
// the body stage (the two-swell brown sea, sox-format to stdout, no fade), the
// velvet floor stage (ffmpeg-fed, attenuation FIRST so the bright band cannot
// clip, its barely-there tide-synced swell, the shared reverb), and the unity
// mixer (-v 1 per input — plain -m scales by 1/n and would halve the preset).
func TestOceanBrownStages(t *testing.T) {
	p, err := mustFindDefault(t, "ocean brown").normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !p.floorActive() {
		t.Fatal("ocean brown should carry the velvet floor")
	}
	wantBody := "--buffer 131072 -c 2 --null -t sox - synth 100 brownnoise band -n 1786 499 tremolo 0.08 37 tremolo 0.02 45 reverb 19 repeat 863"
	if got := strings.Join(p.bodySoxArgs(), " "); got != wantBody {
		t.Errorf("body stage:\n got %q\nwant %q", got, wantBody)
	}
	wantFloor := "--buffer 131072 -t wav - -t sox - gain -30 band -n 4500 2000 tremolo 0.02 18 reverb 19"
	if got := strings.Join(p.floorSoxArgs(), " "); got != wantFloor {
		t.Errorf("floor stage:\n got %q\nwant %q", got, wantFloor)
	}
	wantMix := "--buffer 131072 --no-show-progress -m -v 1 -t sox /dev/fd/3 -v 1 -t sox /dev/fd/4 fade t 3"
	if got := strings.Join(p.mixerArgs(), " "); got != wantMix {
		t.Errorf("mixer:\n got %q\nwant %q", got, wantMix)
	}
}

// TestOceanPinkBodyStage pins the swapped-roles preset: pink bed, the big slow
// wave a separate brown layer trimmed −7, overall +7 restoring brown's
// reference level — Dan's "make the brown the bigger wave and the pink the
// smaller", exactly as auditioned.
func TestOceanPinkBodyStage(t *testing.T) {
	p, err := mustFindDefault(t, "ocean pink").normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	wantBody := "--buffer 131072 -c 2 --null -t sox - synth 100 brownnoise gain -7 tremolo 0.02 45 synth pinknoise mix band -n 1786 499 tremolo 0.08 37 reverb 19 repeat 863 gain 7"
	if got := strings.Join(p.bodySoxArgs(), " "); got != wantBody {
		t.Errorf("body stage:\n got %q\nwant %q", got, wantBody)
	}
	if got := strings.Join(p.floorSoxArgs(), " "); !strings.Contains(got, "gain -30") || !strings.Contains(got, "tremolo 0.02 18") {
		t.Errorf("ocean pink should share the velvet floor tuning, got %q", got)
	}
}

// TestNormalizeFloor pins the floor validation: unknown colours rejected,
// bright-band defaults filled, a swell-less floor stays perfectly still, and
// clearing the type clears every floor field.
func TestNormalizeFloor(t *testing.T) {
	if _, err := (noisePreset{Type: "brown", FloorType: "mauve"}).normalize(); err == nil {
		t.Error("an unknown floor colour must be rejected")
	}
	p, err := (noisePreset{Type: "brown", FloorType: "velvet", FloorSpeed: 0.02}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.FloorType != "velvetnoise" {
		t.Errorf("bare floor colour not suffixed: %q", p.FloorType)
	}
	if p.FloorCenterHz != 4500 || p.FloorWidthHz != 2000 || p.FloorDepth != 18 {
		t.Errorf("floor defaults not filled: %g/%g depth %g", p.FloorCenterHz, p.FloorWidthHz, p.FloorDepth)
	}
	p, err = (noisePreset{Type: "brown", FloorType: "white"}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.FloorSpeed != 0 || p.FloorDepth != 0 {
		t.Errorf("a swell-less floor should stay still, got %g/%g", p.FloorSpeed, p.FloorDepth)
	}
	if got := strings.Join(p.floorSoxArgs(), " "); strings.Contains(got, "tremolo") || !strings.Contains(got, "synth 60 whitenoise") {
		t.Errorf("still native floor stage wrong: %q", got)
	}
	p, err = (noisePreset{Type: "brown", FloorCenterHz: 9999}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.FloorCenterHz != 0 {
		t.Error("floor fields should clear when no floor colour is set")
	}
}
