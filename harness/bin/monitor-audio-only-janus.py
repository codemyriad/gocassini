#!/usr/bin/env python3
"""Observe SFU outbound media for new subscribers in an isolated harness stack.

Records counters and forwarding state only; excludes SDP/ICE credentials.
"""
import argparse
import json
import signal
import time
import urllib.error
import urllib.request
from pathlib import Path

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--output", required=True)
parser.add_argument("--ready-file", required=True)
parser.add_argument("--url", default="http://127.0.0.1:17088/admin")
# Committed local harness secret; never use this monitor against production.
parser.add_argument("--secret", default="01e2fcd0d226d7f4cf34a8a61397f110693f05042e57ab68e94f8476a4b8f22a")
args = parser.parse_args()
running = True


def stop(*_):
    global running
    running = False


signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)


def request(path, operation):
    payload = json.dumps({"janus": operation, "transaction": "audio-only-check", "admin_secret": args.secret}).encode()
    req = urllib.request.Request(args.url + path, data=payload, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=3) as response:
        result = json.load(response)
    if result.get("janus") != "success":
        raise RuntimeError(f"Janus admin {operation} failed")
    return result


def handles():
    for session in request("", "list_sessions")["sessions"]:
        for handle in request(f"/{session}", "list_handles")["handles"]:
            yield session, handle


baseline = set(handles())
Path(args.ready_file).touch()
samples = {}
errors = 0
while running:
    try:
        for session, handle in handles():
            if (session, handle) in baseline:
                continue
            info = request(f"/{session}/{handle}", "handle_info")["info"]
            plugin = info.get("plugin_specific", {})
            if plugin.get("type") != "subscriber" or not plugin.get("answered"):
                continue
            observed = samples.setdefault(str(handle), {"samples": 0, "audio_packets": 0, "video_packets": 0, "video_forwarding": False, "audio_offered": False})
            observed["samples"] += 1
            for stream in plugin.get("streams", []):
                if stream.get("type") == "audio":
                    observed["audio_offered"] = True
                if stream.get("type") == "video" and stream.get("send") and stream.get("ready"):
                    observed["video_forwarding"] = True
            for media in info.get("webrtc", {}).get("media", {}).values():
                kind = media.get("type")
                if kind in ("audio", "video"):
                    key = kind + "_packets"
                    observed[key] = max(observed[key], media.get("stats", {}).get("out", {}).get("packets", 0))
    except (urllib.error.URLError, RuntimeError, KeyError):
        # A handle can disappear between list_handles and handle_info.
        errors += 1
    time.sleep(0.25)
Path(args.output).write_text(json.dumps({"subscribers": samples, "sampling_errors": errors}, indent=2) + "\n")
if not any(s["audio_packets"] > 0 for s in samples.values()) or not all((not s["audio_offered"] or s["audio_packets"] > 0) and s["video_packets"] == 0 and not s["video_forwarding"] for s in samples.values()):
    raise SystemExit("FAIL: SFU must forward audio where offered, and zero video for every subscription")
print(f"PASS: {sum(s['audio_packets'] > 0 for s in samples.values())} audio subscribers, {sum(not s['audio_offered'] for s in samples.values())} waiting subscriptions, zero video")
