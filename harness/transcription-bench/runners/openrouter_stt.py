#!/usr/bin/env python3
"""Benchmark a hosted STT model on the verified private meeting fixtures."""
from __future__ import annotations

import argparse
import base64
import hashlib
import http.client
import io
import json
import os
from pathlib import Path
import ssl
import sys
import time
import wave

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "scripts"))
from benchlib import atomic_json_write, verify_fixtures  # noqa: E402

SCHEMA = "gocassini.openrouter-stt-benchmark.v1"


def canonical_wav(path, info):
    """Correct streaming WAV headers without changing a single PCM byte."""
    with path.open("rb") as stream:
        stream.seek(info.data_offset)
        pcm = stream.read(info.data_bytes)
    output = io.BytesIO()
    with wave.open(output, "wb") as wav:
        wav.setnchannels(info.channels)
        wav.setsampwidth(info.bits_per_sample // 8)
        wav.setframerate(info.sample_rate_hz)
        wav.writeframes(pcm)
    return output.getvalue()


def summarize(runs):
    warm = [r for r in runs if not r["warmup"] and r["status"] == 200]
    audio = sum(r["audioSeconds"] for r in warm)
    wall = sum(r["seconds"] for r in warm)
    return {"audioSeconds": audio, "wallSeconds": wall,
            "rtMultiplier": audio / wall if wall else None,
            "reportedCostUSD": sum(r.get("usage", {}).get("cost", 0) for r in runs),
            "requests": len(runs)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--audio-dir", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--model", default="microsoft/mai-transcribe-2")
    parser.add_argument("--passes", type=int, default=3)
    parser.add_argument("--max-cost-usd", type=float, default=0.10)
    args = parser.parse_args()
    if not 1 <= args.passes <= 5 or args.max_cost_usd <= 0:
        parser.error("require 1–5 measured passes and a positive cost bound")
    key = os.environ.get("OPENROUTER_API_KEY")
    if not key:
        parser.error("OPENROUTER_API_KEY is required")
    fixtures = verify_fixtures(args.manifest, args.audio_dir)
    settings = {"model": args.model, "response_format": "verbose_json",
                "timestamp_granularities": ["word"],
                "provider": {"options": {"azure": {"enhancedMode": {
                    "modelOptions": {"transcribeStyle": "verbatim"}}}}}}
    payloads = {}
    metadata = []
    for fixture in fixtures:
        raw = canonical_wav(Path(fixture["path"]), fixture["wav"])
        payloads[fixture["name"]] = json.dumps(settings | {"input_audio": {
            "data": base64.b64encode(raw).decode(), "format": "wav"}}).encode()
        metadata.append({"name": fixture["name"], "audioSeconds": fixture["wav"].duration_seconds,
                         "canonicalWavSha256": hashlib.sha256(raw).hexdigest()})
    report = {"schema": SCHEMA, "model": args.model, "settings": settings,
              "fixtureManifestSha256": hashlib.sha256(args.manifest.read_bytes()).hexdigest(),
              "fixtures": metadata, "status": "running", "results": [], "runs": [],
              "timingBoundary": "request upload through complete response; one discarded full pass; persistent HTTP client; provider warm state unobservable"}
    connection = http.client.HTTPSConnection("openrouter.ai", timeout=90, context=ssl.create_default_context())
    headers = {"Authorization": "Bearer " + key, "Content-Type": "application/json"}
    try:
        for pass_no in range(args.passes + 1):
            for fixture in metadata:
                if summarize(report["runs"])["reportedCostUSD"] >= args.max_cost_usd:
                    raise RuntimeError("reported spend bound reached")
                start = time.perf_counter()
                connection.request("POST", "/api/v1/audio/transcriptions",
                                   body=payloads[fixture["name"]], headers=headers)
                response = connection.getresponse()
                raw = response.read()
                seconds = time.perf_counter() - start
                data = json.loads(raw)
                run = {"audio": fixture["name"], "audioSeconds": fixture["audioSeconds"],
                       "pass": pass_no, "warmup": pass_no == 0, "seconds": seconds,
                       "status": response.status, "generationID": response.getheader("X-Generation-Id"),
                       "usage": data.get("usage", {}), "text": data.get("text", ""),
                       "wordTimestamps": len(data.get("words", []))}
                report["runs"].append(run)
                if response.status != 200 or not isinstance(data.get("text"), str):
                    raise RuntimeError(f"STT request failed with HTTP {response.status}")
                if pass_no == 1:
                    report["results"].append({"audio": fixture["name"], "label": args.model,
                                              "text": data["text"]})
                report["summary"] = summarize(report["runs"])
                atomic_json_write(args.output, report)
            print(f"pass {pass_no}: {len(metadata)} meeting fixtures completed", flush=True)
        report["status"] = "completed"
    except Exception as error:
        report["status"] = "failed"
        report["errorType"] = type(error).__name__
        raise
    finally:
        connection.close()
        report["summary"] = summarize(report["runs"])
        atomic_json_write(args.output, report)
    print(json.dumps(report["summary"]))


if __name__ == "__main__":
    main()
