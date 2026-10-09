#!/usr/bin/env python3
"""Negative controls for video admitted only after the rejoin boundary."""
import copy
import importlib.util
import json
import signal
import subprocess
import sys
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("verify_rejoin", Path(__file__).with_name("verify-audio-only-rejoin.py"))
verifier = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verifier)


class RejoinPolicyTests(unittest.TestCase):
    def setUp(self):
        self.probe = {"streams": [{"codec_type": "audio"}, {"codec_type": "attachment"}]}
        self.source = {"capture_mode": "audio-only", "logical_tracks": [{"kind": "audio"}], "packet_streams": [{"codec": "audio/opus"}]}
        subscriber = {"audio_packets": 10, "video_packets": 0, "video_forwarding": False}
        self.janus = {"subscribers": {
            "before": dict(subscriber, first_audio_wall_ns=100, last_audio_wall_ns=190),
            "after": dict(subscriber, first_audio_wall_ns=210, last_audio_wall_ns=220),
        }}

    def validate(self):
        verifier.validate(self.probe, self.source, self.janus, 200)

    def test_audio_in_both_phases(self):
        self.validate()

    def test_reused_subscriber(self):
        self.janus["subscribers"] = {"same": dict(self.janus["subscribers"]["before"], last_audio_wall_ns=220)}
        self.validate()

    def test_video_after_rejoin_is_rejected_at_every_layer(self):
        for layer in ("output", "logical_track", "packet_stream", "sfu_packets", "sfu_forwarding"):
            with self.subTest(layer=layer):
                self.setUp()
                if layer == "output":
                    self.probe["streams"].append({"codec_type": "video"})
                elif layer == "logical_track":
                    self.source["logical_tracks"].append({"kind": "video"})
                elif layer == "packet_stream":
                    self.source["packet_streams"].append({"codec": "video/VP8"})
                elif layer == "sfu_packets":
                    self.janus["subscribers"]["after"]["video_packets"] = 1
                else:
                    self.janus["subscribers"]["after"]["video_forwarding"] = True
                with self.assertRaises(AssertionError):
                    self.validate()

    def test_each_phase_requires_sfu_audio(self):
        for phase in ("before", "after"):
            with self.subTest(phase=phase):
                self.setUp()
                del self.janus["subscribers"][phase]
                with self.assertRaises(AssertionError):
                    self.validate()

    def test_missing_or_empty_evidence_fails(self):
        for target, key in ((self.probe, "streams"), (self.source, "logical_tracks"), (self.source, "packet_streams"), (self.janus, "subscribers")):
            with self.subTest(key=key):
                original = copy.deepcopy(target[key])
                target[key] = {} if key == "subscribers" else []
                with self.assertRaises(AssertionError):
                    self.validate()
                target[key] = original

    def test_wrong_policy_fails(self):
        self.source["capture_mode"] = "audio-video"
        with self.assertRaises(AssertionError):
            self.validate()


class MonitorTests(unittest.TestCase):
    def test_monitor_spans_rejoin_and_rejects_late_video(self):
        for late_video in (False, True):
            with self.subTest(late_video=late_video), tempfile.TemporaryDirectory() as tmp:
                phase = 0
                polls = {1: 0, 2: 0}

                class Handler(BaseHTTPRequestHandler):
                    def log_message(self, *_):
                        pass

                    def do_POST(self):
                        operation = json.loads(self.rfile.read(int(self.headers["Content-Length"])))["janus"]
                        result = {"janus": "success"}
                        if operation == "list_sessions":
                            result["sessions"] = [1]
                        elif operation == "list_handles":
                            result["handles"] = [phase] if phase else []
                        else:
                            current = int(self.path.rsplit("/", 1)[1])
                            polls[current] += 1
                            result["info"] = {
                                "plugin_specific": {"type": "subscriber", "answered": True, "streams": [{"type": "audio"}]},
                                "webrtc": {"media": {
                                    "0": {"type": "audio", "stats": {"out": {"packets": polls[current]}}},
                                    "1": {"type": "video", "stats": {"out": {"packets": int(late_video and current == 2)}}},
                                }},
                            }
                        data = json.dumps(result).encode()
                        self.send_response(200)
                        self.send_header("Content-Length", str(len(data)))
                        self.end_headers()
                        self.wfile.write(data)

                server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
                thread = threading.Thread(target=server.serve_forever, daemon=True)
                thread.start()
                output, ready = Path(tmp) / "janus.json", Path(tmp) / "ready"
                process = subprocess.Popen([
                    sys.executable, str(Path(__file__).with_name("monitor-audio-only-janus.py")),
                    "--url", f"http://127.0.0.1:{server.server_port}/admin",
                    "--output", str(output), "--ready-file", str(ready),
                ], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

                def wait_for(predicate):
                    deadline = time.monotonic() + 5
                    while not predicate():
                        if process.poll() is not None or time.monotonic() >= deadline:
                            self.fail("monitor did not reach expected phase")
                        time.sleep(0.02)

                try:
                    wait_for(ready.exists)
                    phase = 1
                    wait_for(lambda: polls[1] >= 2)
                    boundary = time.time_ns()
                    phase = 2
                    wait_for(lambda: polls[2] >= 2)
                    process.send_signal(signal.SIGTERM)
                    stdout, stderr = process.communicate(timeout=5)
                    self.assertEqual(process.returncode, int(late_video), stdout + stderr)
                    evidence = json.loads(output.read_text())
                    self.assertLess(evidence["subscribers"]["1"]["first_audio_wall_ns"], boundary)
                    self.assertGreaterEqual(evidence["subscribers"]["2"]["last_audio_wall_ns"], boundary)
                    self.assertEqual(evidence["subscribers"]["2"]["video_packets"], int(late_video))
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.communicate()
                    server.shutdown()
                    server.server_close()
                    thread.join()


if __name__ == "__main__":
    unittest.main()
