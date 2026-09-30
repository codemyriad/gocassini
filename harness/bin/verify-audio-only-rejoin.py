#!/usr/bin/env python3
"""Require audio-only source/output and SFU audio on both sides of a rejoin."""
import argparse
import json
import subprocess
from pathlib import Path


def validate(probe, source, janus, boundary):
    kinds = [stream.get("codec_type") for stream in probe["streams"]]
    assert "audio" in kinds and "video" not in kinds, "final output must contain audio and zero video"
    assert source.get("capture_mode") == "audio-only", "source capture policy is not audio-only"
    tracks = source["logical_tracks"]
    assert tracks and all(t.get("kind") == "audio" for t in tracks), "source contains non-audio tracks"
    streams = source["packet_streams"]
    assert streams and all(s.get("codec", "").startswith("audio/") for s in streams), "source contains non-audio packet streams"
    subscribers = list(janus["subscribers"].values())
    assert subscribers, "no SFU subscriber evidence"
    assert all(s["video_packets"] == 0 and not s["video_forwarding"] for s in subscribers), "SFU forwarded video"
    assert any(0 < s.get("first_audio_wall_ns", 0) < boundary for s in subscribers), "no SFU audio observed before rejoin"
    assert any(s.get("last_audio_wall_ns", 0) >= boundary for s in subscribers), "no SFU audio observed after rejoin"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input", required=True)
    parser.add_argument("--session", required=True)
    parser.add_argument("--janus", required=True)
    parser.add_argument("--boundary-ns", required=True, type=int)
    args = parser.parse_args()
    probe = json.loads(subprocess.check_output(["ffprobe", "-v", "error", "-show_streams", "-of", "json", args.input]))
    validate(probe, json.loads(Path(args.session).read_text()), json.loads(Path(args.janus).read_text()), args.boundary_ns)
    print("PASS: audio-only source/output and SFU audio before/after rejoin, zero video forwarding")


if __name__ == "__main__":
    main()
