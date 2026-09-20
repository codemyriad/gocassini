#!/usr/bin/env python3

from __future__ import annotations

import json
import struct
import sys
import tempfile
import unittest
from pathlib import Path

RUNNERS_DIR = Path(__file__).resolve().parent.parent / "runners"
sys.path.insert(0, str(RUNNERS_DIR))

from benchlib import (
    STREAMING_WAV_SENTINELS,
    aligned_edit_metrics,
    iter_result_cases,
    iter_result_texts,
    load_manifest,
    phrase_present,
    read_wav_info,
    select_fixture_names,
)
from score_results import discover_result_paths, load_ground_truth, observed_direct_speech
from voxtral_offline import extract_json_object, parse_attribution_response


def make_wav(path: Path, declared_data_bytes: int, frames: int = 800) -> None:
    samples = b"\x00\x00" * frames
    fmt = struct.pack("<HHIIHH", 1, 1, 16000, 32000, 2, 16)
    body = b"fmt " + struct.pack("<I", len(fmt)) + fmt
    body += b"data" + struct.pack("<I", declared_data_bytes) + samples
    riff_size = 0xFFFFFFFF if declared_data_bytes in STREAMING_WAV_SENTINELS else len(body) + 4
    path.write_bytes(b"RIFF" + struct.pack("<I", riff_size) + b"WAVE" + body)


class WavInfoTest(unittest.TestCase):
    def test_normal_wav(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "normal.wav"
            make_wav(path, 1600)
            info = read_wav_info(path)
            self.assertEqual(info.frames, 800)
            self.assertAlmostEqual(info.duration_seconds, 0.05)

    def test_streaming_wav_sentinels_use_actual_file_length(self) -> None:
        for sentinel in sorted(STREAMING_WAV_SENTINELS):
            with self.subTest(sentinel=sentinel), tempfile.TemporaryDirectory() as temporary:
                path = Path(temporary) / "stream.wav"
                make_wav(path, sentinel)
                info = read_wav_info(path)
                self.assertEqual(info.declared_data_bytes, sentinel)
                self.assertEqual(info.data_bytes, 1600)
                self.assertEqual(info.frames, 800)

    def test_truncated_declared_data_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "bad.wav"
            make_wav(path, 1800)
            with self.assertRaisesRegex(ValueError, "only 1600 remain"):
                read_wav_info(path)


class HelpersTest(unittest.TestCase):
    def test_result_directory_discovers_only_supported_payloads(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            nested = root / "run" / "worker"
            nested.mkdir(parents=True)
            supported = nested / "worker.json"
            supported.write_text(
                json.dumps({"schema": "gocassini.voxtral-offline-benchmark.v2"}),
                encoding="utf-8",
            )
            (root / "vast-state.json").write_text(
                json.dumps({"schema": "gocassini.vast-transcription-state.v1"}),
                encoding="utf-8",
            )
            direct = root / "direct.json"
            direct.write_text(
                json.dumps({"schema": "gocassini.voxtral-realtime-benchmark.v2"}),
                encoding="utf-8",
            )

            self.assertEqual(
                discover_result_paths([direct], [root]),
                [direct, supported],
            )

    def test_phrase_matching_is_case_and_punctuation_insensitive(self) -> None:
        self.assertTrue(phrase_present("The RAM-spikes stopped", "RAM spikes"))
        self.assertFalse(phrase_present("telegram spikes", "RAM spikes"))

    def test_fixture_selection(self) -> None:
        manifest = {"fixtures": [{"name": "a.wav"}, {"name": "b.wav"}]}
        self.assertEqual(select_fixture_names(manifest, "all"), ["a.wav", "b.wav"])
        self.assertEqual(select_fixture_names(manifest, "b.wav"), ["b.wav"])
        with self.assertRaisesRegex(ValueError, "unknown fixtures"):
            select_fixture_names(manifest, "c.wav")

    def test_structured_result_extraction(self) -> None:
        payload = {
            "schema": "gocassini.voxtral-realtime-benchmark.v2",
            "results": [{"audio": "x/a.wav", "label": "delay-480", "text": "hello"}],
        }
        self.assertEqual(list(iter_result_texts(payload)), [("a.wav", "delay-480", "hello")])
        payload["schema"] = "gocassini.voxtral-realtime-benchmark.v1"
        self.assertEqual(list(iter_result_texts(payload)), [("a.wav", "delay-480", "hello")])

    def test_attribution_json_accepts_fence_preamble_and_braces_in_text(self) -> None:
        response = """Analysis follows.\n```json
        {"verbatim_transcript":"Chima said {okay}.",
         "direct_speech_intervals_by_track":{"Chima":[{"start":0.1,"end":1.2}],"Ivan":[]},
         "overlap_intervals":[{"start":0.8,"end":1.0}],
         "confidence":0.91,
         "audio_quality":{"echo":"low","cross_track_bleed":"audible"}}
        ```\nDone."""
        parsed, error = extract_json_object(response)
        self.assertIsNone(error)
        self.assertEqual(parsed["verbatim_transcript"], "Chima said {okay}.")
        observations = parse_attribution_response(response)
        self.assertEqual(observations["transcript"], "Chima said {okay}.")
        self.assertFalse(observations["directSpeech"])
        self.assertEqual(observations["directSpeechTrack"], "Ivan")
        self.assertEqual(observations["overlapCount"], 1)
        self.assertEqual(observations["confidence"], 0.91)
        self.assertEqual(observations["structuredValidationErrors"], [])

    def test_attribution_json_supports_wrapped_and_camel_case_output(self) -> None:
        response = json.dumps(
            {
                "result": {
                    "verbatimTranscript": "Okay.",
                    "directSpeechIntervalsByTrack": {
                        "Chima": [],
                        "Ivan": {"directSpeech": True, "intervals": []},
                    },
                    "overlapIntervals": [],
                    "audioQuality": {"echo": "none"},
                }
            }
        )
        observations = parse_attribution_response(response)
        self.assertEqual(observations["transcript"], "Okay.")
        self.assertTrue(observations["directSpeech"])
        self.assertEqual(observations["overlapCount"], 0)

    def test_malformed_attribution_is_visible_and_not_scored_as_json_prose(self) -> None:
        observations = parse_attribution_response("```json\n{not valid}\n```")
        self.assertEqual(observations["transcript"], "")
        self.assertIsNone(observations["directSpeech"])
        self.assertIsNone(observations["overlapCount"])
        self.assertIn("could not parse attribution JSON", observations["structuredParseError"])
        self.assertIsNone(
            observed_direct_speech(
                {"challengeCase": "short-okay", "directSpeech": None},
                observations["transcript"],
            )
        )
        self.assertFalse(observed_direct_speech({"directSpeech": None}, ""))

    def test_multi_track_challenge_maps_to_mix_and_surfaces_observations(self) -> None:
        payload = {
            "schema": "gocassini.voxtral-offline-benchmark.v2",
            "results": [
                {
                    "audio": ["clips/mix.wav", "clips/chima.wav", "clips/ivan.wav"],
                    "mixFixture": "clips/mix.wav",
                    "label": "three-track-disputed-interjection",
                    "challengeCase": "disputed-interjection",
                    "text": "raw JSON must not become the transcript",
                    "transcript": "Reviewed words.",
                    "overlapIntervals": [{"start": 1.0, "end": 1.2}],
                    "overlapCount": 1,
                    "directSpeech": False,
                    "directSpeechTrack": "Ivan",
                    "structuredParseError": None,
                    "structuredValidationErrors": [],
                }
            ],
        }
        case = list(iter_result_cases(payload))[0]
        self.assertEqual(case["fixture"], "mix.wav")
        self.assertEqual(case["challengeCase"], "disputed-interjection")
        self.assertEqual(case["sourceAudio"], ["mix.wav", "chima.wav", "ivan.wav"])
        self.assertEqual(case["text"], "Reviewed words.")
        self.assertFalse(case["directSpeech"])
        self.assertEqual(case["directSpeechTrack"], "Ivan")
        self.assertEqual(case["overlapCount"], 1)

    def test_legacy_multi_track_result_is_no_longer_dropped(self) -> None:
        payload = {
            "schema": "gocassini.voxtral-benchmark.v1",
            "results": [
                {
                    "audio": ["mix.wav", "chima.wav", "ivan.wav"],
                    "label": "three-track-old",
                    "text": "legacy response",
                }
            ],
        }
        case = list(iter_result_cases(payload))[0]
        self.assertEqual(case["fixture"], "mix.wav")
        self.assertEqual(case["text"], "legacy response")
        self.assertEqual(case["challengeCase"], "three-track-old")

    def test_manifest_rejects_path_traversal_fixture_name(self) -> None:
        manifest = {
            "schema": "gocassini.transcription-fixtures.v1",
            "fixtureSet": "test",
            "audioFormat": {"codec": "pcm_s16le", "sampleRateHz": 16000, "channels": 1},
            "globalForbiddenTerms": ["absent"],
            "fixtures": [{"name": "../escape.wav"}],
        }
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "manifest.json"
            path.write_text(json.dumps(manifest), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "unsafe fixture basename"):
                load_manifest(path)

    def test_aligned_edit_metrics(self) -> None:
        metrics = aligned_edit_metrics("one two three", "one wrong three now")
        self.assertEqual(metrics["tokenEditDistance"], 2)
        self.assertEqual(metrics["alignedTokens"], 2)
        self.assertAlmostEqual(metrics["wordErrorRate"], 2 / 3)
        self.assertAlmostEqual(metrics["alignedTokenRecall"], 2 / 3)

    def test_ground_truth_shape_and_structured_observations(self) -> None:
        manifest = {
            "fixtureSet": "test-set",
            "fixtures": [{"name": "a.wav", "expectedDirectSpeech": False}],
        }
        value = {
            "schema": "gocassini.transcription-ground-truth.v1",
            "fixtureSet": "test-set",
            "fixtures": {
                "a.wav": {"referenceTranscript": "", "overlapCount": 0, "directSpeech": False}
            },
        }
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "truth.json"
            path.write_text(json.dumps(value), encoding="utf-8")
            loaded = load_ground_truth(path, manifest)
            self.assertFalse(loaded["a.wav"]["directSpeech"])
        payload = {
            "schema": "gocassini.voxtral-offline-benchmark.v2",
            "results": [
                {
                    "audio": ["a.wav"],
                    "label": "structured",
                    "text": "hello",
                    "overlapCount": 2,
                    "directSpeech": True,
                }
            ],
        }
        case = list(iter_result_cases(payload))[0]
        self.assertEqual(case["overlapCount"], 2)
        self.assertTrue(case["directSpeech"])


if __name__ == "__main__":
    unittest.main()
