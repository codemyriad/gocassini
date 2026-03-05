#!/usr/bin/env -S uv run --script
# /// script
# requires-python = ">=3.11"
# dependencies = []
# ///
from __future__ import annotations

import argparse
import array
import json
import math
import subprocess
import sys
from dataclasses import dataclass
from pathlib import Path


def run_command(cmd: list[str]) -> bytes:
    result = subprocess.run(cmd, capture_output=True, check=False)
    if result.returncode != 0:
        stderr = result.stderr.decode("utf-8", errors="replace").strip()
        raise RuntimeError(f"command failed ({' '.join(cmd)}): {stderr}")
    return result.stdout


def probe_audio_count(path: Path) -> int:
    raw = run_command(
        [
            "ffprobe",
            "-v",
            "error",
            "-show_streams",
            "-of",
            "json",
            str(path),
        ]
    )
    parsed = json.loads(raw.decode("utf-8"))
    streams = parsed.get("streams") or []
    return len([s for s in streams if str(s.get("codec_type")) == "audio"])


def extract_pcm_mono(path: Path, sample_rate: int, stream_spec: str | None = None, filter_complex: str | None = None) -> list[float]:
    cmd = ["ffmpeg", "-v", "error", "-i", str(path)]
    if filter_complex:
        cmd.extend(["-filter_complex", filter_complex, "-map", "[mix]"])
    else:
        if stream_spec:
            cmd.extend(["-map", stream_spec])
    cmd.extend(["-ac", "1", "-ar", str(sample_rate), "-f", "s16le", "-"])
    payload = run_command(cmd)
    values = array.array("h")
    values.frombytes(payload)
    if sys.byteorder != "little":
        values.byteswap()
    return [v / 32768.0 for v in values]


def source_mix_filter(audio_count: int) -> str:
    if audio_count <= 0:
        raise RuntimeError("source has no audio tracks")
    if audio_count == 1:
        return "[0:a:0]anull[mix]"
    labels = "".join(f"[0:a:{i}]" for i in range(audio_count))
    return f"{labels}amix=inputs={audio_count}:duration=longest:normalize=0,aresample=async=1[mix]"


def rms_envelope(samples: list[float], samples_per_bin: int) -> list[float]:
    if samples_per_bin <= 0:
        raise RuntimeError("samples_per_bin must be > 0")
    if not samples:
        return []
    out: list[float] = []
    for start in range(0, len(samples), samples_per_bin):
        chunk = samples[start : start + samples_per_bin]
        if not chunk:
            continue
        power = sum(x * x for x in chunk) / float(len(chunk))
        out.append(math.sqrt(power))
    return out


def zscore(values: list[float]) -> list[float]:
    if not values:
        return []
    mean = sum(values) / float(len(values))
    var = sum((x - mean) ** 2 for x in values) / float(len(values))
    std = math.sqrt(max(var, 1e-12))
    return [(x - mean) / std for x in values]


def corr_at_shift(a: list[float], b: list[float], shift: int) -> float:
    if not a or not b:
        return -1.0
    if shift >= 0:
        a0 = 0
        b0 = shift
    else:
        a0 = -shift
        b0 = 0
    n = min(len(a) - a0, len(b) - b0)
    if n <= 8:
        return -1.0
    acc = 0.0
    for i in range(n):
        acc += a[a0 + i] * b[b0 + i]
    return acc / float(n)


@dataclass(frozen=True)
class LagResult:
    shift_bins: int
    corr: float

    @property
    def shift_sec(self) -> float:
        return 0.0


def best_lag(a: list[float], b: list[float], max_shift_bins: int) -> LagResult:
    best = LagResult(shift_bins=0, corr=-1.0)
    for shift in range(-max_shift_bins, max_shift_bins + 1):
        c = corr_at_shift(a, b, shift)
        if c > best.corr:
            best = LagResult(shift_bins=shift, corr=c)
    return best


def window(values: list[float], start: int, length: int) -> list[float]:
    if start >= len(values) or length <= 0:
        return []
    return values[start : min(len(values), start + length)]


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Check composed audio sync against source multitrack mix.")
    parser.add_argument("--source", required=True, help="Multitrack source MKV.")
    parser.add_argument("--composed", required=True, help="Composed MP4.")
    parser.add_argument("--sample-rate", type=int, default=8000)
    parser.add_argument("--bin-ms", type=int, default=100)
    parser.add_argument("--max-shift-sec", type=float, default=30.0)
    parser.add_argument("--max-abs-lag-sec", type=float, default=1.0)
    parser.add_argument("--max-drift-sec", type=float, default=1.5)
    parser.add_argument("--min-corr", type=float, default=0.20)
    parser.add_argument("--report", default=None, help="Optional report JSON path.")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    source = Path(args.source)
    composed = Path(args.composed)
    if not source.is_file():
        print(f"source not found: {source}", file=sys.stderr)
        return 2
    if not composed.is_file():
        print(f"composed not found: {composed}", file=sys.stderr)
        return 2

    audio_count = probe_audio_count(source)
    if audio_count <= 0:
        print("source has no audio streams", file=sys.stderr)
        return 2

    mix = extract_pcm_mono(
        path=source,
        sample_rate=args.sample_rate,
        filter_complex=source_mix_filter(audio_count),
    )
    comp = extract_pcm_mono(
        path=composed,
        sample_rate=args.sample_rate,
        stream_spec="0:a:0",
    )

    samples_per_bin = max(1, int(args.sample_rate * args.bin_ms / 1000.0))
    mix_env = zscore(rms_envelope(mix, samples_per_bin))
    comp_env = zscore(rms_envelope(comp, samples_per_bin))
    if not mix_env or not comp_env:
        print("audio extraction produced empty envelopes", file=sys.stderr)
        return 2

    max_shift_bins = max(1, int(args.max_shift_sec * 1000.0 / args.bin_ms))
    global_lag = best_lag(mix_env, comp_env, max_shift_bins)
    bin_sec = args.bin_ms / 1000.0
    global_lag_sec = global_lag.shift_bins * bin_sec

    win_bins = max(1, int(120.0 / bin_sec))
    mid_start = max(0, min(len(mix_env), len(comp_env)) // 2 - (win_bins // 2))
    tail_start = max(0, min(len(mix_env), len(comp_env)) - win_bins)
    starts = [0, mid_start, tail_start]
    local_lags: list[float] = []
    for start in starts:
        a = window(mix_env, start, win_bins)
        b = window(comp_env, start, win_bins)
        if not a or not b:
            continue
        local = best_lag(a, b, max_shift_bins)
        local_lags.append(local.shift_bins * bin_sec)
    drift_sec = (max(local_lags) - min(local_lags)) if len(local_lags) >= 2 else 0.0

    report = {
        "source": str(source),
        "composed": str(composed),
        "audio_stream_count": audio_count,
        "sample_rate": args.sample_rate,
        "bin_ms": args.bin_ms,
        "global_lag_sec": round(global_lag_sec, 6),
        "global_corr": round(global_lag.corr, 6),
        "local_lags_sec": [round(x, 6) for x in local_lags],
        "drift_sec": round(drift_sec, 6),
        "thresholds": {
            "max_abs_lag_sec": args.max_abs_lag_sec,
            "max_drift_sec": args.max_drift_sec,
            "min_corr": args.min_corr,
        },
    }

    if args.report:
        Path(args.report).write_text(json.dumps(report, indent=2), encoding="utf-8")

    print(json.dumps(report, indent=2))

    ok = (
        abs(global_lag_sec) <= args.max_abs_lag_sec
        and drift_sec <= args.max_drift_sec
        and global_lag.corr >= args.min_corr
    )
    if not ok:
        print("sync verification failed", file=sys.stderr)
        return 1
    print("sync verification passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

