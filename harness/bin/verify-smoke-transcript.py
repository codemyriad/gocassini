#!/usr/bin/env python3
"""Quality assertion only: consumes a transcript; never runs or caches transcription."""
import argparse
import json
import math
import re
import sys
import unicodedata

parser = argparse.ArgumentParser(description="Assert the LibriSpeech smoke transcript's quality.")
parser.add_argument("transcript")
parser.add_argument("--expected", default='the english forwarded to the french baskets of flowers of which they had made a plentiful provision to greet the arrival of the young princess the french in return invited the english to a supper which was to be given the next day')
parser.add_argument("--minimum", type=float, default=0.50)
args = parser.parse_args()
transcript_path, expected, min_ratio = args.transcript, args.expected, args.minimum
if not math.isfinite(min_ratio) or not 0 < min_ratio <= 1:
    parser.error("minimum must be finite and in (0, 1]")

def normalise(text: str) -> str:
    text = unicodedata.normalize("NFKD", text)
    text = "".join(c for c in text if not unicodedata.combining(c))
    text = text.lower()
    text = re.sub(r"[^a-z0-9 ]+", " ", text)
    text = re.sub(r"\s+", " ", text).strip()
    return text

def lev(a: str, b: str) -> int:
    if len(a) < len(b):
        a, b = b, a
    if not b:
        return len(a)
    prev = list(range(len(b) + 1))
    for i, ca in enumerate(a, 1):
        curr = [i]
        for j, cb in enumerate(b, 1):
            curr.append(min(
                curr[j-1] + 1,
                prev[j] + 1,
                prev[j-1] + (0 if ca == cb else 1),
            ))
        prev = curr
    return prev[-1]

with open(transcript_path) as f:
    data = json.load(f)

words = []
for segment in data.get("segments", []):
    for w in segment.get("words", []):
        text = (w.get("text") or "").strip()
        if text:
            words.append(text)
got = normalise(" ".join(words))
want = normalise(expected)

if not got:
    print("[v3-verify] FAIL transcript is empty after normalisation")
    sys.exit(1)

distance = lev(got, want)
max_len = max(len(got), len(want))
ratio = 1.0 - (distance / max_len) if max_len else 0.0

print(f"[v3-verify] expected: {want!r}")
print(f"[v3-verify] got:      {got!r}")
print(f"[v3-verify] edit-distance={distance} max-len={max_len} ratio={ratio:.4f} threshold={min_ratio:.2f}")

if ratio < min_ratio:
    print(f"[v3-verify] FAIL Levenshtein ratio {ratio:.4f} < threshold {min_ratio:.2f}")
    sys.exit(1)
print(f"[v3-verify] OK   Levenshtein ratio {ratio:.4f} >= threshold {min_ratio:.2f}")
