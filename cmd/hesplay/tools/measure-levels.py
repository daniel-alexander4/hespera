#!/usr/bin/env python3
"""Measure how loud each noise preset actually gets, from the SHIPPED argv.

The house rule is that a level is measured through the pipeline that plays, not
derived on paper — `band -n` saturates, `-m` scales, and reverb adds tail, so a
number worked out from the gain fields is not the number a listener hears.

Loudness here is the **P95 of 0.4 s short-time RMS**, not mean RMS: these presets
swell, sometimes by 16 dB or more, and what a listener judges is how loud it gets
rather than its average. Mean RMS makes a deep-swelling preset look far quieter
than it sounds.

    python3 cmd/hesplay/tools/measure-levels.py                # entrainment family
    python3 cmd/hesplay/tools/measure-levels.py rain campfire  # named presets

Plays nothing: every pipeline is redirected to a file.

⚠️ A preset with NO layers (`sharp focus (40 Hz)`) prints one process with no
`<(…)` stage, so splitting on the last `)` silently fails on it — hence the
two branches in render().
"""
import os
import re
import subprocess
import sys
import tempfile
import wave

import numpy as np

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", ".."))

# The entrainment family, reference first. Every one of these should land within
# about half a dB of the reference — see cmd/hesplay/CLAUDE.md.
FAMILY = [
    "body asleep, mind awake (4+10 Hz)",
    "body asleep, mind awake (4+10 Hz, monaural)",
    "deep hypnagogia (3.5+10 Hz)",
    "deep hypnagogia (3.5+10 Hz, monaural)",
    "deep sleep (2 Hz)",
    "relaxed alert (10 Hz)",
    "sharp focus (40 Hz)",
    "sleep onset (10→4 Hz)",
    "drowsy (6 Hz)",
    "drowsy (6 Hz, timbral)",
]
WINDOW_SECS = 120


def build(tmp):
    """Build hesplay from the working tree, so this measures what you just edited."""
    binary = os.path.join(tmp, "hesplay")
    subprocess.run(["go", "build", "-o", binary, "./cmd/hesplay"], cwd=REPO, check=True)
    return binary


def printed(binary, cfg, name):
    """The shipped pipeline for one preset. An empty config dir forces the built-ins."""
    env = dict(os.environ, XDG_CONFIG_HOME=cfg)
    out = subprocess.run([binary, "noise", "--print", name],
                         capture_output=True, text=True, env=env).stdout
    # A program preset prints one pipeline per phase; the first is the one that
    # sets the level, and the phases inherit it.
    for line in out.splitlines():
        if line.startswith("play "):
            return line
    return None


def render(line, out):
    """Turn a `play` pipeline into a file render. Nothing reaches the audio device."""
    line = re.sub(r"repeat \d+", "repeat 2", line)  # three buffers, not 24 hours
    i = line.rfind(")")
    if i < 0:
        j = line.index("--null") + len("--null")            # no layers: one process
        cmd = f"sox {line[5:j]} -b 16 {out} {line[j:].strip()} trim 0 {WINDOW_SECS}"
    else:
        cmd = (f"sox {line[5:i + 1]} -b 16 {out} "
               f"{line[i + 1:].strip()} trim 0 {WINDOW_SECS}")
    return subprocess.run(["bash", "-c", cmd], capture_output=True).returncode == 0


def stats(path):
    w = wave.open(path)
    sr = w.getframerate()
    d = np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16).astype(float)
    d = d.reshape(-1, w.getnchannels()).mean(1) / 32768.0
    win = int(sr * 0.4)
    short = np.array([np.sqrt((d[i:i + win] ** 2).mean()) for i in range(0, len(d) - win, win)])
    short = short[short > 0]
    db = lambda x: 20 * np.log10(x)
    return db(np.sqrt((d ** 2).mean())), db(abs(d).max()), db(np.percentile(short, 95))


def main():
    names = sys.argv[1:] or FAMILY
    with tempfile.TemporaryDirectory() as tmp:
        binary = build(tmp)
        cfg = os.path.join(tmp, "cfg")          # empty: a box's own hesplay.json
        os.makedirs(cfg)                        # would otherwise shadow the built-ins
        print(f"{'preset':44s} {'RMS':>8s} {'peak':>8s} {'P95':>8s}   vs first")
        ref = None
        for n in names:
            line = printed(binary, cfg, n)
            if not line:
                print(f"{n:44s}  no such preset")
                continue
            path = os.path.join(tmp, "m.wav")
            if not render(line, path):
                print(f"{n:44s}  render failed")
                continue
            rms, peak, p95 = stats(path)
            if ref is None:
                ref = p95
            print(f"{n:44s} {rms:8.2f} {peak:8.2f} {p95:8.2f}   {p95 - ref:+6.2f} dB")


if __name__ == "__main__":
    main()
