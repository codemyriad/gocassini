#!/usr/bin/env python3
"""Check relative decoded sound against timestamped live publisher turns."""
import argparse
import array
import json
import math
import re
import statistics
import subprocess
import sys


def decode(path, stream=None):
    args = ["ffmpeg", "-v", "error", "-copyts", "-i", path]
    if stream is not None:
        args += ["-map", f"0:{stream}", "-af", "aresample=async=1:first_pts=0"]
    args += ["-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "pipe:1"]
    samples = array.array("h", subprocess.check_output(args))
    if sys.byteorder != "little":
        samples.byteswap()
    return samples


def onsets(samples, frequency):
    coefficient = 2 * math.cos(2 * math.pi * frequency / 16000)
    starts, active = [], False
    for pos in range(0, len(samples) - 319, 320):
        prev = prev2 = 0.0
        for sample in samples[pos:pos + 320]:
            current = sample / 32768 + coefficient * prev - prev2
            prev2, prev = prev, current
        amplitude = 2 * math.sqrt(max(0, prev * prev + prev2 * prev2 - coefficient * prev * prev2)) / 320
        present = amplitude > 0.015
        if present and not active:
            starts.append(pos / 16000)
        active = present
    return starts


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--source", required=True)
    parser.add_argument("--published", required=True)
    parser.add_argument("--publisher-log", required=True)
    args = parser.parse_args()
    probe = json.loads(subprocess.check_output(["ffprobe", "-v", "error", "-show_streams", "-of", "json", args.source]))
    streams = [s for s in probe["streams"] if s["codec_type"] == "audio"]
    assert len(streams) == 2, "two independent speakers required"
    origin = min(int(s["tags"]["FIRST_TIMELINE_NS"]) for s in streams) / 1e9
    turns = {440: [], 880: []}
    with open(args.publisher_log) as handle:
        for line in handle:
            match = re.search(r"audible=CassiniGoE2E([12]) active=2 .*wall_ns=(\d+)", line)
            if match:
                frequency = 440 if match[1] == "1" else 880
                turns[frequency].append(int(match[2]) / 1e9 - origin)
    mixed = decode(args.published)
    assert len(mixed) / 16000 >= 90, "live fixture must cover at least 90 seconds"
    shifts = []
    evidence = {}
    for frequency in turns:
        # Select by sound, because participant display names can arrive after
        # stream opening; also assert that only one track contains each tone.
        tracks = [onsets(decode(args.source, s["index"]), frequency) for s in streams]
        matching = [track for track in tracks if len(track) >= 4]
        assert len(matching) == 1, f"{frequency}Hz is missing or duplicated: {tracks}"
        source = matching[0]
        published = onsets(mixed, frequency)
        assert len(source) == len(published), f"{frequency}Hz lost turns during publication"
        assert max(abs(a - b) for a, b in zip(source, published)) <= 0.04 + 1e-6, "source/published placement differs by >40ms"
        expected = [turn for turn in turns[frequency] if max(15, source[0] + 5) <= turn <= published[-1] + 1]
        assert len(expected) >= 8 and expected[-1] - expected[0] >= 60, "missing early/late publisher landmarks"
        errors = [min(published, key=lambda onset: abs(onset - turn)) - turn for turn in expected]
        assert all(abs(error) <= 1 for error in errors), "a whole publisher turn is absent"
        shifts.extend(errors)
        evidence[str(frequency)] = {"turns": len(expected), "first_s": expected[0], "last_s": expected[-1], "errors_s": errors}
    common_shift = statistics.median(shifts)
    max_error = max(abs(error - common_shift) for error in shifts)
    assert max_error <= 0.2, f"relative sound error {max_error:.3f}s exceeds 200ms after one common shift"
    print(json.dumps({"result": "PASS", "common_shift_s": common_shift, "max_relative_error_s": max_error, "speakers": evidence}, indent=2))


if __name__ == "__main__":
    main()
