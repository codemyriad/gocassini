#!/usr/bin/env python3
from __future__ import annotations

import math
from dataclasses import dataclass
from typing import Sequence


@dataclass(frozen=True)
class AudioDetection:
    ok: bool
    expected_sec: float
    detected_sec: float
    ratio: float
    rms: float


@dataclass(frozen=True)
class VideoDetection:
    ok: bool
    expected_sec: float
    detected_sec: float
    peak_luma: float


def goertzel_power(samples: Sequence[float], sample_rate: int, target_hz: float) -> float:
    if not samples or sample_rate <= 0 or target_hz <= 0:
        return 0.0
    n = len(samples)
    omega = (2.0 * math.pi * target_hz) / float(sample_rate)
    coeff = 2.0 * math.cos(omega)
    s_prev = 0.0
    s_prev2 = 0.0
    for sample in samples:
        s = sample + coeff * s_prev - s_prev2
        s_prev2 = s_prev
        s_prev = s
    power = s_prev2 * s_prev2 + s_prev * s_prev - coeff * s_prev * s_prev2
    return max(0.0, power / max(1, n))


def window_rms(samples: Sequence[float]) -> float:
    if not samples:
        return 0.0
    acc = 0.0
    for sample in samples:
        acc += sample * sample
    return math.sqrt(acc / float(len(samples)))


def _window(samples: Sequence[float], sample_rate: int, center_sec: float, window_sec: float) -> list[float]:
    half = max(0.01, window_sec / 2.0)
    start = int(max(0.0, (center_sec - half) * sample_rate))
    end = int(min(len(samples), (center_sec + half) * sample_rate))
    if end <= start:
        return []
    return list(samples[start:end])


def detect_audio_event(
    samples: Sequence[float],
    sample_rate: int,
    expected_sec: float,
    target_hz: float,
    all_frequencies_hz: Sequence[float],
    search_sec: float = 0.8,
    step_sec: float = 0.02,
    window_sec: float = 0.24,
    min_ratio: float = 1.8,
    min_rms: float = 0.008,
) -> AudioDetection:
    best_ratio = -1.0
    best_time = expected_sec
    best_rms = 0.0

    steps = int(max(1, round((2.0 * search_sec) / step_sec)))
    for idx in range(steps + 1):
        shift = -search_sec + idx * step_sec
        center = expected_sec + shift
        if center <= 0:
            continue

        chunk = _window(samples, sample_rate, center, window_sec)
        if len(chunk) < int(sample_rate * 0.04):
            continue

        target_power = goertzel_power(chunk, sample_rate, target_hz)
        other_max = 0.0
        for freq in all_frequencies_hz:
            if abs(freq - target_hz) < 0.1:
                continue
            other_max = max(other_max, goertzel_power(chunk, sample_rate, freq))

        ratio = target_power / (other_max + 1e-12)
        rms = window_rms(chunk)

        if ratio > best_ratio:
            best_ratio = ratio
            best_time = center
            best_rms = rms

    ok = best_ratio >= min_ratio and best_rms >= min_rms
    return AudioDetection(
        ok=ok,
        expected_sec=expected_sec,
        detected_sec=best_time,
        ratio=max(0.0, best_ratio),
        rms=best_rms,
    )


def detect_video_pulse(
    luma_values: Sequence[float],
    fps: float,
    expected_sec: float,
    search_sec: float = 0.8,
    min_luma_delta: float = 12.0,
) -> VideoDetection:
    if not luma_values or fps <= 0:
        return VideoDetection(False, expected_sec, expected_sec, 0.0)

    sorted_luma = sorted(luma_values)
    base_idx = int(0.3 * (len(sorted_luma) - 1))
    baseline = sorted_luma[max(0, base_idx)]

    start_idx = max(0, int((expected_sec - search_sec) * fps))
    end_idx = min(len(luma_values), int((expected_sec + search_sec) * fps) + 1)
    if end_idx <= start_idx:
        return VideoDetection(False, expected_sec, expected_sec, baseline)

    peak_idx = start_idx
    peak_value = luma_values[start_idx]
    for idx in range(start_idx + 1, end_idx):
        if luma_values[idx] > peak_value:
            peak_value = luma_values[idx]
            peak_idx = idx

    detected_sec = peak_idx / fps
    ok = (peak_value - baseline) >= min_luma_delta
    return VideoDetection(ok, expected_sec, detected_sec, peak_value)
