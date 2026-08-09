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
	// By NAME, not by position: "brown" is the reference chain this test guards,
	// and the shipped list is ordered for the phone (Dan put theta surf on top),
	// so an index here would break on a reorder that changes no argv at all.
	p, err := mustFindDefault(t, "brown").normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
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

// TestOceanBrownArgs pins the shipped two-swell sea as ONE plain SoX stage:
// chained tremolos with the offset trim between them, and no floor at all (Dan
// took the velvet layer off it 2026-08-08) — so it plays through the single-
// process path, not the body/floor/mixer topology.
func TestOceanBrownArgs(t *testing.T) {
	p, err := mustFindDefault(t, "ocean brown").normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.layered() {
		t.Fatal("ocean brown should carry no extra layer")
	}
	want := "--buffer 131072 --no-show-progress -c 2 --null synth 106.25 brownnoise band -n 1786 499 tremolo 0.08 37 trim 6.25 tremolo 0.02 45 reverb 19 repeat 863 fade t 3"
	if got := strings.Join(p.noiseArgs(), " "); got != want {
		t.Errorf("noise args:\n got %q\nwant %q", got, want)
	}
}

// TestOceanPinkStages pins the three-wave preset's process topology (Dan,
// 2026-08-08: "large brown, medium brown and then a small pink"). Each wave is
// its OWN stage, which is the point — one stream could only nest the envelopes.
// Stage order is fd order, and each stage's synth runs its offset longer than
// its own whole-period loop. The mixer takes one unity input per stage (-v 1;
// plain -m scales by 1/n and would quietly shrink the preset) and owns the fade.
func TestOceanPinkStages(t *testing.T) {
	p, err := mustFindDefault(t, "ocean pink").normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(p.Layers) != 2 {
		t.Fatalf("ocean pink should carry two extra waves, got %d", len(p.Layers))
	}
	// The body is the SMALL pink — the phase reference, hence no trim — and
	// carries the preset's output gain.
	wantBody := "--buffer 131072 -c 2 --null -t sox - synth 62.5 pinknoise band -n 1786 499 tremolo 0.08 37 reverb 19 repeat 1382 gain -0.5"
	if got := strings.Join(p.bodySoxArgs(), " "); got != wantBody {
		t.Errorf("body stage:\n got %q\nwant %q", got, wantBody)
	}
	// The large (50s) and medium (25s) brown waves: the preset's own band
	// inherited, each one's own swell, its offset trim, the shared reverb — and
	// the gain LAST, where the body puts it, because `band -n` saturates and a
	// gain ahead of it lands ~15 dB off what the same number means on the body.
	wantLayers := []string{
		"--buffer 131072 -c 2 --null -t sox - synth 112.5 brownnoise band -n 1786 499 tremolo 0.02 45 trim 12.5 reverb 19 repeat 863 gain -6.5",
		"--buffer 131072 -c 2 --null -t sox - synth 81.25 brownnoise band -n 1786 499 tremolo 0.04 40 trim 6.25 reverb 19 repeat 1151 gain -9.5",
	}
	for i, want := range wantLayers {
		if got := strings.Join(p.layerSoxArgs(p.Layers[i]), " "); got != want {
			t.Errorf("layer %d stage:\n got %q\nwant %q", i+1, got, want)
		}
	}
	wantMix := "--buffer 131072 --no-show-progress -m -v 1 -t sox /dev/fd/3 -v 1 -t sox /dev/fd/4 -v 1 -t sox /dev/fd/5 fade t 3"
	if got := strings.Join(p.mixerArgs(), " "); got != wantMix {
		t.Errorf("mixer:\n got %q\nwant %q", got, wantMix)
	}
}

// TestOceanBrownBrownBrown pins the all-brown preset's three stages: one colour,
// three sizes, and a TONE LADDER stepping about a third of an octave per size
// (large 1420, medium 1786, small 2250) because three waves in one band have
// identical timbre and leave the ear only rhythm to separate them.
//
// Two things here are easy to break by tidying and must not be:
//   - the medium layer states 1786 EXPLICITLY. The body is the small wave, so an
//     empty layer band inherits the preset's 2250 and the medium wave silently
//     follows the small one up the ladder.
//   - the gains absorb the band shift (large −8.4, small −6.0, medium unchanged
//     at −9.5). Brown falls ~6 dB/octave, so equal numbers across unequal bands
//     would deliver the tone change as a balance change.
func TestOceanBrownBrownBrown(t *testing.T) {
	p, err := mustFindDefault(t, "ocean brown brown brown").normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.Type != "brownnoise" || len(p.Layers) != 2 {
		t.Fatalf("want a brown body plus two brown layers, got %q + %d layers", p.Type, len(p.Layers))
	}
	wantBody := "--buffer 131072 -c 2 --null -t sox - synth 64 brownnoise band -n 2250 499 tremolo 0.0625 85 reverb 19 repeat 1349 gain -6"
	if got := strings.Join(p.bodySoxArgs(), " "); got != wantBody {
		t.Errorf("bright wave (body):\n got %q\nwant %q", got, wantBody)
	}
	wantLayers := []string{
		"--buffer 131072 -c 2 --null -t sox - synth 90 brownnoise band -n 1420 499 tremolo 0.025 45 trim 10 reverb 19 repeat 1079 gain -6.4",
		"--buffer 131072 -c 2 --null -t sox - synth 81.25 brownnoise band -n 1786 499 tremolo 0.04 40 trim 6.25 reverb 19 repeat 1151 gain -9.5",
	}
	for i, want := range wantLayers {
		if got := strings.Join(p.layerSoxArgs(p.Layers[i]), " "); got != want {
			t.Errorf("layer %d:\n got %q\nwant %q", i+1, got, want)
		}
	}
	// The ladder itself, stated as properties rather than as numbers. Tone falls
	// as the wave grows — and the RATES deliberately do not follow it: the
	// brightest wave is no longer the fastest, which is what keeps the three from
	// reading as one texture running at three speeds.
	if !(p.Layers[0].CenterHz < p.Layers[1].CenterHz && p.Layers[1].CenterHz < p.CenterHz) {
		t.Errorf("tone should rise as the wave shrinks, got large %g / medium %g / small %g",
			p.Layers[0].CenterHz, p.Layers[1].CenterHz, p.CenterHz)
	}
	// Periods still fall with the wave's size (large slowest, bright fastest),
	// but both ends have moved INWARD from the 50s/12.5s they started at: the
	// bright swell is drawn out and the dark one tightened, so the ratio between
	// the fastest and slowest wave is no longer 4:1.
	if !(p.Layers[0].Speed < p.Layers[1].Speed && p.Layers[1].Speed < p.WaveSpeed) {
		t.Errorf("swell period should fall with wave size, got large %g / medium %g / bright %g Hz",
			p.Layers[0].Speed, p.Layers[1].Speed, p.WaveSpeed)
	}
	if p.WaveSpeed >= 0.08 {
		t.Errorf("the bright wave's swell should last longer than the original 12.5s, got %g Hz", p.WaveSpeed)
	}
	if p.Layers[0].Speed <= 0.02 {
		t.Errorf("the dark wave's swell should be shorter than the original 50s, got %g Hz", p.Layers[0].Speed)
	}
	// Every wave keeps a distinct period, or two of them beat as one rhythm.
	for i, a := range []float64{p.WaveSpeed, p.Layers[0].Speed, p.Layers[1].Speed} {
		for j, b := range []float64{p.WaveSpeed, p.Layers[0].Speed, p.Layers[1].Speed} {
			if i < j && a == b {
				t.Errorf("two waves share a swell rate (%g) — they would read as one", a)
			}
		}
	}
}

// TestEntrainmentPulse pins the pulse's ONE placement rule on all three
// topologies: past `repeat` and past the mixer's inputs, so it modulates the
// endless stream rather than the looped buffer. That is what frees its rate from
// the loop-seam derivation every swell is bound by — get this wrong and a 6 Hz
// modulation would click at every buffer boundary.
func TestEntrainmentPulse(t *testing.T) {
	// native single stage: after repeat, before gain and fade
	p, err := (noisePreset{Type: "brown", WaveSpeed: 0.08, GainDB: -3, FadeIn: 5, PulseHz: 6, PulseDepth: 20}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	got := strings.Join(p.noiseArgs(), " ")
	if !strings.Contains(got, "repeat 1382 tremolo 6 20 gain -3 fade t 5") {
		t.Errorf("native pulse misplaced: %q", got)
	}
	// hybrid (ffmpeg-fed, no repeat at all): after the shaping, before gain/fade
	h, err := (noisePreset{Type: "velvet", WaveSpeed: 0.08, FadeIn: 5, PulseHz: 4.5}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := strings.Join(h.soxPipeArgs(), " "); !strings.Contains(got, "reverb 19 tremolo 4.5 20 fade t 5") {
		t.Errorf("hybrid pulse misplaced: %q", got)
	}
	// layered: at the MIXER, after every input — one rhythm across every wave,
	// which a per-stage pulse could not give (it would fade with its own wave)
	l, err := (noisePreset{Type: "brown", WaveSpeed: 0.08, FadeIn: 3, PulseHz: 6, PulseDepth: 25,
		Layers: []noiseLayer{{Type: "brown", Speed: 0.02}}}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := strings.Join(l.mixerArgs(), " "); !strings.HasSuffix(got, "/dev/fd/4 tremolo 6 25 fade t 3") {
		t.Errorf("mixer pulse misplaced: %q", got)
	}
	// and no stage carries it — the body stage is level-only
	if got := strings.Join(l.bodySoxArgs(), " "); strings.Contains(got, "tremolo 6") {
		t.Errorf("the pulse must not ride a single stage: %q", got)
	}
	// off by default, and a silly value switches it off rather than clamping on
	if q, _ := (noisePreset{Type: "brown", WaveSpeed: 0.08}).normalize(); q.pulseArgs() != nil {
		t.Error("the pulse must be off unless asked for")
	}
	if q, _ := (noisePreset{Type: "brown", PulseHz: -3, PulseDepth: 40}).normalize(); q.PulseHz != 0 || q.PulseDepth != 0 {
		t.Errorf("a negative rate should switch the pulse off, got %g/%g", q.PulseHz, q.PulseDepth)
	}
	if q, _ := (noisePreset{Type: "brown", PulseHz: 900}).normalize(); q.PulseHz != 40 {
		t.Errorf("rate should clamp to the entrainment span, got %g", q.PulseHz)
	}
	if q, _ := (noisePreset{Type: "brown", PulseHz: 6}).normalize(); q.PulseDepth != 20 {
		t.Errorf("a depthless pulse should default shallow, got %g", q.PulseDepth)
	}
}

// TestThetaSurf pins the hypnagogic preset: a 6 Hz pulse on the whole mix, and a
// sea deliberately duller than the coast presets — a lower ladder (high
// frequencies are the alerting part of a noise), a small wave that no longer
// vanishes and returns, and a long fade for someone already lying down.
func TestThetaSurf(t *testing.T) {
	p, err := mustFindDefault(t, "drowsy (6 Hz)").normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.PulseHz != 6 || p.PulseDepth != 10 {
		t.Errorf("want a 6 Hz / 10%% pulse, got %g Hz / %g%%", p.PulseHz, p.PulseDepth)
	}
	if !strings.HasSuffix(strings.Join(p.mixerArgs(), " "), "tremolo 6 10 fade t 12") {
		t.Errorf("the pulse should ride the summed sea: %v", p.mixerArgs())
	}
	coast, err := mustFindDefault(t, "ocean brown brown brown").normalize()
	if err != nil {
		t.Fatalf("normalize the coast preset: %v", err)
	}
	if !(p.CenterHz < coast.CenterHz && p.Layers[0].CenterHz < coast.Layers[0].CenterHz) {
		t.Error("theta surf should sit lower than the coast preset, not brighter")
	}
	if p.WaveDepth >= coast.WaveDepth {
		t.Errorf("the small wave should be steadier here, got depth %g vs %g", p.WaveDepth, coast.WaveDepth)
	}
	if p.FadeIn <= coast.FadeIn {
		t.Errorf("this preset starts on someone lying down; want a longer fade, got %g", p.FadeIn)
	}
	if coast.PulseHz != 0 {
		t.Error("the coast presets must carry no pulse")
	}
}

// TestEntrainmentNamedByState pins the naming convention (Dan, 2026-08-09: "label
// the entrainments not by frequency, but by the state they put you in. add
// frequency in parentheses") and, more importantly, the lookup that makes those
// names usable: a name is what a schedule window stores and what the CLI resolves,
// and every one of these would otherwise need shell quoting.
func TestEntrainmentNamedByState(t *testing.T) {
	cfg := defaultNoiseConfig()
	for _, raw := range cfg.Presets {
		p, err := raw.normalize()
		if err != nil {
			t.Fatalf("normalize %q: %v", raw.Name, err)
		}
		if p.Category != "pulse" && p.Category != "binaural" && p.Category != "monaural" {
			continue
		}
		// Every entrainment preset states its rate in parentheses, and states a
		// state outside them — "6 Hz" alone would be the thing this rename undid.
		if !strings.Contains(p.Name, "(") || !strings.Contains(p.Name, "Hz)") {
			t.Errorf("%q should carry its frequency in parentheses", p.Name)
		}
		if before := strings.TrimSpace(strings.Split(p.Name, "(")[0]); before == "" {
			t.Errorf("%q is only a frequency; it should name the state", p.Name)
		}
	}
	// The lookup: exact, then parenthetical-insensitive, then unambiguous prefix.
	for _, q := range []string{"deep sleep (2 Hz)", "deep sleep", "DEEP SLEEP", "deep sl"} {
		p, err := cfg.findPreset(q)
		if err != nil {
			t.Errorf("findPreset(%q): %v", q, err)
		} else if p.Name != "deep sleep (2 Hz)" {
			t.Errorf("findPreset(%q) found %q", q, p.Name)
		}
	}
	// An ambiguous prefix must NOT silently pick one.
	if _, err := cfg.findPreset("deep"); err == nil {
		t.Error(`"deep" matches two presets and should be refused, not guessed`)
	} else if !strings.Contains(err.Error(), "several") {
		t.Errorf("ambiguity should say so: %v", err)
	}
	if _, err := cfg.findPreset("nonesuch"); err == nil {
		t.Error("an unknown name should still be an error")
	}
}

// TestMonauralLayer pins the speaker-valid form of a carrier pair: both tones in
// BOTH channels, so the beat happens acoustically instead of neurally. The
// headphones flag is the property that must follow it — a monaural preset marked
// "headphones" would send someone hunting for a cable it doesn't need.
func TestMonauralLayer(t *testing.T) {
	p, err := (noisePreset{Type: "brown", WaveSpeed: 0.08,
		Layers: []noiseLayer{{CarrierHz: 200, BeatHz: 10, Gain: -32.5, Monaural: true}}}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	got := strings.Join(p.layerSoxArgs(p.Layers[0]), " ")
	want := "--buffer 131072 -c 2 --null -t sox - synth 60 sine 200 synth sine mix 210 gain -32.5 repeat 1439"
	if got != want {
		t.Errorf("monaural stage:\n got %q\nwant %q", got, want)
	}
	if p.needsHeadphones() {
		t.Error("a monaural pair beats in the air — it must NOT be marked headphones")
	}
	// The binaural form of the same layer does need them.
	q, _ := (noisePreset{Type: "brown", Layers: []noiseLayer{{CarrierHz: 200, BeatHz: 10}}}).normalize()
	if !q.needsHeadphones() {
		t.Error("one carrier per ear requires headphones")
	}
	// Monaural is a property of a carrier pair only; on a noise layer it clears.
	r, _ := (noisePreset{Type: "brown", Layers: []noiseLayer{{Type: "pink", Speed: 0.02, Monaural: true}}}).normalize()
	if r.Layers[0].Monaural {
		t.Error("a noise layer cannot be monaural")
	}
}

// TestIsochronicPulse pins the hard-gate shape. The depth inversion is the part
// worth guarding: SoX's square amod takes an OFFSET, where 0 gates fully on/off
// and 100 does nothing, so depth maps to 100−offset.
func TestIsochronicPulse(t *testing.T) {
	p, err := (noisePreset{Type: "brown", WaveSpeed: 0.08, PulseHz: 40, PulseDepth: 12, PulseShape: "square"}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if got := strings.Join(p.pulseArgs(), " "); got != "synth square amod 40 88" {
		t.Errorf("isochronic gate: got %q", got)
	}
	// It sits in the same place the sine pulse does — past `repeat`, on the
	// endless stream, so the gate has no loop seam to click at.
	if got := strings.Join(p.noiseArgs(), " "); !strings.Contains(got, "repeat 1382 synth square amod 40 88") {
		t.Errorf("gate misplaced: %q", got)
	}
	// A sine pulse is unchanged, and an unknown shape falls back to it rather
	// than refusing to play.
	if q, _ := (noisePreset{Type: "brown", PulseHz: 6, PulseDepth: 10}).normalize(); strings.Join(q.pulseArgs(), " ") != "tremolo 6 10" {
		t.Errorf("sine pulse changed: %v", q.pulseArgs())
	}
	if q, _ := (noisePreset{Type: "brown", PulseHz: 6, PulseShape: "sawtooth"}).normalize(); q.PulseShape != "" {
		t.Errorf("an unknown shape should fall back to sine, got %q", q.PulseShape)
	}
	// Shape without a pulse is meaningless and clears.
	if q, _ := (noisePreset{Type: "brown", PulseShape: "square"}).normalize(); q.PulseShape != "" {
		t.Errorf("shape should clear with no pulse, got %q", q.PulseShape)
	}
}

// TestProgramPhases pins the descending program: what each phase substitutes,
// which phase keeps the fade, and that a phase preset can never recurse into the
// program runner (Phases must be cleared, or runNoise would loop forever).
func TestProgramPhases(t *testing.T) {
	p, err := mustFindDefault(t, "sleep onset (10→4 Hz)").normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(p.Phases) != 5 {
		t.Fatalf("want 5 phases, got %d", len(p.Phases))
	}
	// A program files by the MECHANISM it walks, not as "a program": this one moves
	// a binaural beat, so it belongs with the presets that need headphones.
	if p.Category != "binaural" {
		t.Errorf("a binaural program should file as binaural, got %q", p.Category)
	}
	// A pulse program files under pulse for the same reason.
	if q, _ := (noisePreset{Type: "brown", PulseHz: 6,
		Phases: []noisePhase{{Minutes: 2, PulseHz: 8}}}).normalize(); q.Category != "pulse" {
		t.Errorf("a pulse program should file as pulse, got %q", q.Category)
	}
	// The rates walk down, which is the entire point.
	var rates []float64
	for i, ph := range p.Phases {
		got := p.withPhase(ph, i == 0)
		if len(got.Phases) != 0 {
			t.Fatal("withPhase must clear Phases or runNoise recurses into the program")
		}
		rates = append(rates, got.Layers[0].BeatHz)
	}
	for i := 1; i < len(rates); i++ {
		if rates[i] >= rates[i-1] {
			t.Errorf("phase %d does not descend: %v", i+1, rates)
		}
	}
	if rates[0] != 10 || rates[len(rates)-1] != 4 {
		t.Errorf("want a 10 Hz -> 4 Hz walk, got %v", rates)
	}
	// Only the first phase fades in; a fade per phase would dip to silence at
	// every boundary, which is a hole punched in a sleep program.
	if p.withPhase(p.Phases[0], true).FadeIn != p.FadeIn {
		t.Error("the first phase should keep the preset's fade")
	}
	if p.withPhase(p.Phases[1], false).FadeIn != 0 {
		t.Error("later phases must not fade in")
	}
	// A phase can move the pulse too, and a zero rate means "keep".
	q, _ := (noisePreset{Type: "brown", PulseHz: 6, WaveSpeed: 0.08,
		Phases: []noisePhase{{Minutes: 2, PulseHz: 10}, {Minutes: 2}}}).normalize()
	if got := q.withPhase(q.Phases[0], true).PulseHz; got != 10 {
		t.Errorf("phase should override the pulse, got %g", got)
	}
	if got := q.withPhase(q.Phases[1], false).PulseHz; got != 6 {
		t.Errorf("a zero rate should keep the preset's, got %g", got)
	}
	// A phase with no duration is dropped rather than given a guessed one.
	if r, _ := (noisePreset{Type: "brown", Phases: []noisePhase{{Minutes: 0, PulseHz: 5}}}).normalize(); len(r.Phases) != 0 {
		t.Errorf("a lengthless phase should be dropped, got %+v", r.Phases)
	}
	if p.programMinutes() != 15 {
		t.Errorf("descend should total 15 minutes, got %g", p.programMinutes())
	}
}

// TestNewEntrainmentPresets pins the five additions as a set: each lands in the
// entrainment card, each carries the mechanism it claims, and the headphones mark
// follows the mechanism rather than the card.
func TestNewEntrainmentPresets(t *testing.T) {
	for _, tc := range []struct {
		name       string
		headphones bool
		check      func(p noisePreset) string
	}{
		{"sleep onset (10→4 Hz)", true, func(p noisePreset) string {
			if len(p.Phases) == 0 {
				return "no program"
			}
			return ""
		}},
		{"deep hypnagogia (3.5+10 Hz)", true, func(p noisePreset) string {
			if p.Layers[0].BeatHz != 3.5 {
				return "not the deeper theta"
			}
			return ""
		}},
		{"relaxed alert (10 Hz)", false, func(p noisePreset) string {
			if !p.Layers[0].Monaural {
				return "should be monaural, so it works on speakers"
			}
			return ""
		}},
		{"deep sleep (2 Hz)", true, func(p noisePreset) string {
			if p.Layers[0].BeatHz != 2 {
				return "not 2 Hz"
			}
			if p.FadeIn < 20 {
				return "wants a long fade: it starts on someone lying down"
			}
			return ""
		}},
		{"sharp focus (40 Hz)", false, func(p noisePreset) string {
			if p.PulseShape != "square" || p.PulseHz != 40 {
				return "should be a 40 Hz isochronic gate"
			}
			if p.CenterHz > 1000 {
				return "the bed must be banded low, or a 40 Hz gate is a buzz"
			}
			return ""
		}},
	} {
		p, err := mustFindDefault(t, tc.name).normalize()
		if err != nil {
			t.Errorf("%s: normalize: %v", tc.name, err)
			continue
		}
		if p.Category == "nature" || p.Category == "noise" {
			t.Errorf("%s: filed under %q, not an entrainment card", tc.name, p.Category)
		}
		if p.needsHeadphones() != tc.headphones {
			t.Errorf("%s: needsHeadphones=%v, want %v", tc.name, p.needsHeadphones(), tc.headphones)
		}
		if msg := tc.check(p); msg != "" {
			t.Errorf("%s: %s", tc.name, msg)
		}
	}
}

// TestPresetCategories pins the three cards. The derivation is what matters more
// than the field: every config already on a box predates it, so a preset that
// names no card must still land in the right one.
func TestPresetCategories(t *testing.T) {
	cfg := defaultNoiseConfig()
	got := map[string][]string{}
	for _, raw := range cfg.Presets {
		p, err := raw.normalize()
		if err != nil {
			t.Fatalf("normalize %q: %v", raw.Name, err)
		}
		if !knownCategory(p.Category) {
			t.Errorf("%q landed in an unknown card %q", p.Name, p.Category)
		}
		got[p.Category] = append(got[p.Category], p.Name)
	}
	// Split by MECHANISM, because that is what the listener has to act on: a
	// texture on speakers, tones needing headphones, or tones that don't.
	want := map[string][]string{
		"nature":   {"ocean brown brown brown", "ocean brown", "ocean pink"},
		"noise":    {"brown", "pink", "white", "tpdf", "blue", "violet", "velvet"},
		"pulse":    {"drowsy (6 Hz)", "sharp focus (40 Hz)"},
		"binaural": {"body asleep, mind awake (4+10 Hz)", "sleep onset (10→4 Hz)", "deep hypnagogia (3.5+10 Hz)", "deep sleep (2 Hz)"},
		"monaural": {"relaxed alert (10 Hz)"},
	}
	for card, names := range want {
		if strings.Join(got[card], ", ") != strings.Join(names, ", ") {
			t.Errorf("%s card:\n got %v\nwant %v", card, got[card], names)
		}
	}
	// Every mechanism outranks the sea test: what you need to know first is how
	// the beat is delivered, not that there are waves under it.
	p, _ := (noisePreset{Type: "brown", Wave2Speed: 0.02,
		Layers: []noiseLayer{{CarrierHz: 100, BeatHz: 4}}}).normalize()
	if p.Category != "binaural" {
		t.Errorf("a sea carrying a binaural pair should file as binaural, got %q", p.Category)
	}
	if q, _ := (noisePreset{Type: "brown", Wave2Speed: 0.02,
		Layers: []noiseLayer{{CarrierHz: 100, BeatHz: 4, Monaural: true}}}).normalize(); q.Category != "monaural" {
		t.Errorf("a monaural pair should file as monaural, got %q", q.Category)
	}
	if q, _ := (noisePreset{Type: "brown", Wave2Speed: 0.02, PulseHz: 6}).normalize(); q.Category != "pulse" {
		t.Errorf("a sea carrying a pulse should file as pulse, got %q", q.Category)
	}
	// A preset carrying BOTH forms files under the stricter one: if any layer
	// needs headphones, the preset does, and filing it as monaural would say the
	// opposite.
	if q, _ := (noisePreset{Type: "brown", Layers: []noiseLayer{
		{CarrierHz: 100, BeatHz: 4, Monaural: true},
		{CarrierHz: 250, BeatHz: 10},
	}}).normalize(); q.Category != "binaural" {
		t.Errorf("a mixed preset should file as binaural, got %q", q.Category)
	}
	// Only the binaural one requires headphones, and the UI marks per preset.
	if !p.needsHeadphones() {
		t.Error("a carrier pair requires headphones")
	}
	if q, _ := (noisePreset{Type: "brown", PulseHz: 6}).normalize(); q.needsHeadphones() {
		t.Error("a pulse preset is speaker-valid and must not be marked headphones")
	}
	// "binaural" means itself again, and must not be aliased away.
	if q, _ := (noisePreset{Type: "brown", Category: "binaural"}).normalize(); q.Category != "binaural" {
		t.Errorf("an explicit binaural card should be honoured, got %q", q.Category)
	}
	// A second swell alone makes it a sea, with no layers at all.
	if q, _ := (noisePreset{Type: "brown", Wave2Speed: 0.02, Wave2Depth: 45}).normalize(); q.Category != "nature" {
		t.Errorf("a two-swell preset should file as nature, got %q", q.Category)
	}
	// An explicit card wins over the derivation; garbage falls back to it rather
	// than refusing to play.
	if q, _ := (noisePreset{Type: "brown", Category: "NATURE "}).normalize(); q.Category != "nature" {
		t.Errorf("an explicit card should be honoured (and normalized), got %q", q.Category)
	}
	if q, _ := (noisePreset{Type: "brown", Category: "kitchen sink"}).normalize(); q.Category != "noise" {
		t.Errorf("an unknown card should fall back to the derivation, got %q", q.Category)
	}
	// "entrainment" was a card for one afternoon and is deliberately NOT aliased:
	// a preset saved under it re-derives by mechanism, which is where the split
	// wants it. Aliasing it anywhere would strand every such preset in one card.
	if q, _ := (noisePreset{Type: "brown", PulseHz: 6, Category: "entrainment"}).normalize(); q.Category != "pulse" {
		t.Errorf("a preset saved as entrainment should re-derive, got %q", q.Category)
	}
}

// TestBinauralLayer pins the carrier-pair stage. The subtle interaction is the
// empty Type: a colourless NOISE layer is deliberately dropped ("the editor said
// remove this"), and a binaural layer has no colour at all — so it must be
// recognised before that rule or it would silently vanish.
func TestBinauralLayer(t *testing.T) {
	p, err := (noisePreset{Type: "brown", WaveSpeed: 0.08,
		Layers: []noiseLayer{{CarrierHz: 100, BeatHz: 4, Gain: -30}}}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(p.Layers) != 1 || !p.Layers[0].binaural() {
		t.Fatalf("a binaural layer must survive having no colour, got %+v", p.Layers)
	}
	// One tone per channel, and none of the noise shaping: a band would tilt a
	// pure tone and reverb would smear the channels into each other, which is
	// precisely the phase difference the effect is made of.
	got := strings.Join(p.layerSoxArgs(p.Layers[0]), " ")
	want := "--buffer 131072 -c 2 --null -t sox - synth 60 sine 100 sine 104 gain -30 repeat 1439"
	if got != want {
		t.Errorf("binaural stage:\n got %q\nwant %q", got, want)
	}
	for _, banned := range []string{"band", "tremolo", "reverb"} {
		if strings.Contains(got, banned) {
			t.Errorf("a binaural stage must not carry %q: %q", banned, got)
		}
	}
	// Carriers are clamped into the range where binaural phase-locking works.
	if q, _ := (noisePreset{Type: "brown", Layers: []noiseLayer{{BeatHz: 4, CarrierHz: 9000}}}).normalize(); q.Layers[0].CarrierHz != 1000 {
		t.Errorf("carrier should clamp to 1000 Hz, got %g", q.Layers[0].CarrierHz)
	}
	if q, _ := (noisePreset{Type: "brown", Layers: []noiseLayer{{BeatHz: 4}}}).normalize(); q.Layers[0].CarrierHz != 100 {
		t.Errorf("a carrier-less beat should default to 100 Hz, got %g", q.Layers[0].CarrierHz)
	}
	// Noise-shaped fields are cleared, so a layer edited from noise to binaural
	// cannot keep a stale band or swell that the stage would ignore anyway.
	q, _ := (noisePreset{Type: "brown", Layers: []noiseLayer{
		{Type: "pink", CenterHz: 900, Speed: 0.02, Depth: 40, Offset: 5, CarrierHz: 200, BeatHz: 7}}}).normalize()
	l := q.Layers[0]
	if l.Type != "" || l.CenterHz != 0 || l.Speed != 0 || l.Depth != 0 || l.Offset != 0 {
		t.Errorf("noise fields should clear on a binaural layer: %+v", l)
	}
}

// TestFocus10 pins the Monroe-shaped preset: the brown reference as the bed, plus
// two beats at once — 4 Hz for the body and 10 Hz to hold the mind up. One beat
// alone is a different state, so the pair is the preset.
func TestFocus10(t *testing.T) {
	p, err := mustFindDefault(t, "body asleep, mind awake (4+10 Hz)").normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	brown, err := mustFindDefault(t, "brown").normalize()
	if err != nil {
		t.Fatalf("normalize brown: %v", err)
	}
	// The bed is the brown preset untouched — same band, swell and reverb.
	if p.CenterHz != brown.CenterHz || p.WidthHz != brown.WidthHz ||
		p.WaveSpeed != brown.WaveSpeed || p.WaveDepth != brown.WaveDepth || p.Reverb != brown.Reverb {
		t.Errorf("the bed should be the brown preset: %+v vs %+v", p, brown)
	}
	if len(p.Layers) != 2 {
		t.Fatalf("want two beats, got %d", len(p.Layers))
	}
	beats := []float64{p.Layers[0].BeatHz, p.Layers[1].BeatHz}
	if beats[0] != 4 || beats[1] != 10 {
		t.Errorf("want a 4 Hz body beat and a 10 Hz mind beat, got %v", beats)
	}
	for i, l := range p.Layers {
		if !l.binaural() {
			t.Errorf("layer %d should be binaural: %+v", i+1, l)
		}
		if l.CarrierHz > 1000 {
			t.Errorf("carrier %g Hz is above where binaural beats work", l.CarrierHz)
		}
		// The tones sit under the bed: a full-scale sine measures -3 dB against
		// the bed's -27, so a positive or zero gain here would bury it.
		if l.Gain > -20 {
			t.Errorf("layer %d gain %g would sit on top of the bed", i+1, l.Gain)
		}
	}
	if p.Layers[0].CarrierHz == p.Layers[1].CarrierHz {
		t.Error("the two pairs should use different carriers, or their own difference tones collide")
	}
}

// TestNormalizeLayers pins layer validation: unknown colours rejected, a
// colourless entry dropped rather than fatal, the preset's own band inherited
// when a layer names none, a swell-less layer left perfectly still, and the
// process-count cap enforced.
func TestNormalizeLayers(t *testing.T) {
	if _, err := (noisePreset{Type: "brown", Layers: []noiseLayer{{Type: "mauve"}}}).normalize(); err == nil {
		t.Error("an unknown layer colour must be rejected")
	}
	// An ffmpeg-only colour IS allowed here, unlike a mid-chain Wave2Type: a
	// layer is its own process, so it can be fed by its own generator.
	p, err := (noisePreset{Type: "brown", CenterHz: 900, WidthHz: 200,
		Layers: []noiseLayer{{Type: "velvet", Speed: 0.02}, {Type: ""}}}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(p.Layers) != 1 {
		t.Fatalf("a colourless layer should be dropped, got %d layers", len(p.Layers))
	}
	l := p.Layers[0]
	if l.Type != "velvetnoise" {
		t.Errorf("bare layer colour not suffixed: %q", l.Type)
	}
	if l.CenterHz != 900 || l.WidthHz != 200 {
		t.Errorf("a bandless layer should inherit the preset's band, got %g/%g", l.CenterHz, l.WidthHz)
	}
	if l.Depth != 37 {
		t.Errorf("a depthless swell should inherit the preset's depth, got %g", l.Depth)
	}
	// An ffmpeg-fed layer streams forever: no synth to loop, so no repeat.
	if got := strings.Join(p.layerSoxArgs(l), " "); !strings.HasPrefix(got, "--buffer 131072 -t wav - -t sox -") || strings.Contains(got, "repeat") {
		t.Errorf("ffmpeg-fed layer stage wrong: %q", got)
	}
	// Where the gain sits is measured, not stylistic (see layerSoxArgs): before
	// the band on the ffmpeg path, where it stops the bright band clipping at no
	// cost in level; after it on the native path, where `band -n`'s saturation
	// otherwise makes the same number ~15 dB hotter than it means on the body.
	ffGain := strings.Join(p.layerSoxArgs(noiseLayer{Type: "velvetnoise", CenterHz: 4500, WidthHz: 2000, Gain: -30}), " ")
	if !strings.Contains(ffGain, "gain -30 band") {
		t.Errorf("an ffmpeg-fed layer must attenuate BEFORE the band: %q", ffGain)
	}
	natGain := strings.Join(p.layerSoxArgs(noiseLayer{Type: "brownnoise", CenterHz: 1786, WidthHz: 499, Gain: -6.5}), " ")
	if !strings.HasSuffix(natGain, "gain -6.5") || strings.Contains(natGain, "gain -6.5 band") {
		t.Errorf("a SoX-native layer must apply its gain LAST: %q", natGain)
	}
	p, err = (noisePreset{Type: "brown", Layers: []noiseLayer{{Type: "white"}}}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.Layers[0].Speed != 0 || p.Layers[0].Depth != 0 {
		t.Errorf("a swell-less layer should stay still, got %g/%g", p.Layers[0].Speed, p.Layers[0].Depth)
	}
	if got := strings.Join(p.layerSoxArgs(p.Layers[0]), " "); strings.Contains(got, "tremolo") || !strings.Contains(got, "synth 60 whitenoise") {
		t.Errorf("still native layer stage wrong: %q", got)
	}
	// Every layer is a process AND a buffered loop, so the count is capped.
	many := make([]noiseLayer, noiseMaxLayers+3)
	for i := range many {
		many[i] = noiseLayer{Type: "white"}
	}
	p, err = (noisePreset{Type: "brown", Layers: many}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(p.Layers) != noiseMaxLayers {
		t.Errorf("layer count should cap at %d, got %d", noiseMaxLayers, len(p.Layers))
	}
}

// TestFoldLegacyFloor pins the migration of the pre-`layers` floor fields: they
// become the layer they always described, keeping the FLOOR's own defaults
// (bright 4.5 kHz band, an 18% breath) rather than the generic layer ones, so a
// config written before the generalization keeps sounding the same. The fold is
// idempotent and clears the old fields, so no second representation survives.
func TestFoldLegacyFloor(t *testing.T) {
	p, err := (noisePreset{Type: "brown", FloorType: "velvet", FloorSpeed: 0.02, FloorGain: -30, FloorOffset: 12.5}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if p.FloorType != "" || p.FloorSpeed != 0 || p.FloorGain != 0 || p.FloorOffset != 0 {
		t.Errorf("legacy floor fields should be cleared by the fold: %+v", p)
	}
	if len(p.Layers) != 1 {
		t.Fatalf("the floor should have become one layer, got %d", len(p.Layers))
	}
	l := p.Layers[0]
	if l.Type != "velvetnoise" || l.CenterHz != 4500 || l.WidthHz != 2000 || l.Depth != 18 {
		t.Errorf("floor defaults not carried into the layer: %+v", l)
	}
	if l.Gain != -30 || l.Offset != 12.5 {
		t.Errorf("floor level/offset lost in the fold: %+v", l)
	}
	// Folding twice must not double the layer.
	if again := p.foldLegacyFloor(); len(again.Layers) != 1 {
		t.Errorf("fold is not idempotent: %d layers", len(again.Layers))
	}
	// And the folded floor's stage must still be the argv that was auditioned —
	// velvet is ffmpeg-fed, so the attenuation stays ahead of the band.
	wantFloor := "--buffer 131072 -t wav - -t sox - gain -30 band -n 4500 2000 tremolo 0.02 18 trim 12.5 reverb 19"
	if got := strings.Join(p.layerSoxArgs(l), " "); got != wantFloor {
		t.Errorf("migrated floor stage changed:\n got %q\nwant %q", got, wantFloor)
	}
	// A floor field with no floor colour is inert, not a layer.
	p, err = (noisePreset{Type: "brown", FloorCenterHz: 9999}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if len(p.Layers) != 0 || p.FloorCenterHz != 0 {
		t.Error("floor fields with no floor colour should clear, not make a layer")
	}
}

// TestSwellOffsets pins the phase-offset trims: the synth runs OFFSET seconds
// long and the trim discards that head after the envelope it shifts — chained
// between the two tremolos, layered after the slow layer's tremolo, floor
// after its own — so the post-trim loop is still a whole number of every
// period and the seam stays phase-continuous (measured: a 2.5s trim moved the
// trough by exactly 2.5s across a repeat boundary). An offset without its
// swell normalizes away.
func TestSwellOffsets(t *testing.T) {
	p, err := (noisePreset{Type: "brown", WaveSpeed: 0.08, Wave2Speed: 0.02, Wave2Depth: 45, Wave2Offset: 6.25}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	got := strings.Join(p.noiseArgs(), " ")
	if !strings.Contains(got, "synth 106.25 brownnoise") || !strings.Contains(got, "tremolo 0.08 37 trim 6.25 tremolo 0.02 45") {
		t.Errorf("chained offset argv wrong: %q", got)
	}
	if p, _ := (noisePreset{Type: "brown", Wave2Offset: 9}).normalize(); p.Wave2Offset != 0 {
		t.Errorf("offset without a second swell should clear, got %g", p.Wave2Offset)
	}
	if p, _ := (noisePreset{Type: "brown", Layers: []noiseLayer{{Type: "white", Offset: 9}}}).normalize(); p.Layers[0].Offset != 0 {
		t.Errorf("layer offset without a layer swell should clear, got %g", p.Layers[0].Offset)
	}
	p, err = (noisePreset{Type: "brown", Layers: []noiseLayer{{Type: "white", Speed: 0.02, Depth: 18, Offset: 12.5}}}).normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	lgot := strings.Join(p.layerSoxArgs(p.Layers[0]), " ")
	if !strings.Contains(lgot, "synth 112.5 whitenoise") || !strings.Contains(lgot, "tremolo 0.02 18 trim 12.5") {
		t.Errorf("layer offset argv wrong: %q", lgot)
	}
}
