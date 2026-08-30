#!/usr/bin/env python3
from __future__ import annotations

import argparse
import json
import math
import os
import shutil
import subprocess
import sys
import tempfile
import urllib.request
import wave
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import numpy as np


DEFAULT_SCENARIO = (
    Path(__file__).resolve().parent.parent
    / "scenarios"
    / "synthetic-pied-piper.v1.json"
)
DEFAULT_OUTPUT_DIR = (
    Path(__file__).resolve().parent.parent
    / "media"
    / "processed"
    / "synthetic-pied-piper-v1"
)
DEFAULT_KOKORO_ASSET_DIR = (
    Path(os.environ.get("XDG_CACHE_HOME", str(Path.home() / ".cache")))
    / "gocassini"
    / "kokoro-onnx"
)
KOKORO_MODEL_URL = (
    "https://github.com/thewh1teagle/kokoro-onnx/releases/download/"
    "model-files-v1.0/kokoro-v1.0.int8.onnx"
)
KOKORO_VOICES_URL = (
    "https://github.com/thewh1teagle/kokoro-onnx/releases/download/"
    "model-files-v1.0/voices-v1.0.bin"
)

# On Mac you'd install ffmpeg-full and add it to PATH
# This way we're ensuring the correct binary (ffmpeg-full, with drawtext, is loaded)
FFMPEG_BIN = os.getenv("FFMPEG_BIN", "ffmpeg")
FFPROBE_BIN = os.getenv("FFPROBE_BIN", "ffprobe")


@dataclass(frozen=True)
class Participant:
    participant_id: str
    display_name: str
    role: str
    voice: str
    lang_code: str
    join_delay_seconds: float
    theme_color: str


@dataclass(frozen=True)
class Turn:
    speaker: str
    start_seconds: float
    text: str


class TTSBackend:
    def synthesize(self, participant: Participant, text: str) -> tuple[int, np.ndarray]:
        raise NotImplementedError


class MockBackend(TTSBackend):
    def synthesize(self, participant: Participant, text: str) -> tuple[int, np.ndarray]:
        sample_rate = 24_000
        word_count = max(1, len(text.split()))
        duration_seconds = max(1.2, word_count * 0.26)
        samples = int(round(duration_seconds * sample_rate))
        t = np.linspace(
            0.0, duration_seconds, samples, endpoint=False, dtype=np.float32
        )
        base_freq = {
            "erlich": 172.0,
            "monica": 214.0,
            "richard": 146.0,
            "jack": 196.0,
            "gavin": 124.0,
            "laurie": 238.0,
        }.get(participant.participant_id, 180.0)
        wobble = (0.02 * np.sin(2.0 * math.pi * 2.7 * t)).astype(np.float32)
        phase = 2.0 * math.pi * base_freq * t * (1.0 + wobble)
        carrier = np.sin(phase).astype(np.float32)
        harmonic = (0.35 * np.sin(phase * 2.0 + 0.3)).astype(np.float32)
        envelope = np.ones_like(carrier)
        fade = min(samples // 8, int(0.06 * sample_rate))
        if fade > 0:
            ramp = np.linspace(0.0, 1.0, fade, dtype=np.float32)
            envelope[:fade] = ramp
            envelope[-fade:] = ramp[::-1]
        audio = 0.16 * (carrier + harmonic) * envelope
        return sample_rate, audio.astype(np.float32)


class KokoroBackend(TTSBackend):
    def __init__(self) -> None:
        try:
            from kokoro_onnx import Kokoro
        except ImportError as exc:
            raise SystemExit(
                "kokoro-onnx is not installed. Run via uv with harness/requirements-tts.txt, "
                "or use --backend mock."
            ) from exc
        asset_dir = Path(
            os_environ("KOKORO_ONNX_ASSET_DIR", str(DEFAULT_KOKORO_ASSET_DIR))
        ).resolve()
        model_path, voices_path = ensure_kokoro_assets(asset_dir)
        self._engine: Any = Kokoro(str(model_path), str(voices_path))

    def synthesize(self, participant: Participant, text: str) -> tuple[int, np.ndarray]:
        audio, sample_rate = self._engine.create(
            text,
            voice=participant.voice,
            speed=1.0,
            lang=kokoro_lang(participant.lang_code),
        )
        combined = np.asarray(audio, dtype=np.float32).reshape(-1)
        if combined.size == 0:
            return sample_rate, np.zeros(int(sample_rate * 0.3), dtype=np.float32)
        peak = float(np.max(np.abs(combined))) if combined.size else 0.0
        if peak > 0.98:
            combined = combined / peak * 0.96
        return sample_rate, combined


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Generate meeting-like media fixtures with local TTS."
    )
    parser.add_argument(
        "--scenario", default=str(DEFAULT_SCENARIO), help="path to scenario JSON"
    )
    parser.add_argument(
        "--output-dir",
        default=str(DEFAULT_OUTPUT_DIR),
        help="directory for generated assets",
    )
    parser.add_argument(
        "--backend",
        choices=("kokoro", "mock"),
        default="kokoro",
        help="TTS backend to use; mock is useful for smoke tests",
    )
    parser.add_argument(
        "--force",
        action="store_true",
        help="regenerate outputs even when the manifest already exists",
    )
    return parser.parse_args()


def load_scenario(path: Path) -> tuple[dict[str, Any], list[Participant], list[Turn]]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    participants = [
        Participant(
            participant_id=str(item["id"]),
            display_name=str(item["display_name"]),
            role=str(item.get("role") or ""),
            voice=str(item["voice"]),
            lang_code=str(item.get("lang_code") or "a"),
            join_delay_seconds=float(item.get("join_delay_seconds") or 0.0),
            theme_color=str(item.get("theme_color") or "#60a5fa"),
        )
        for item in raw["participants"]
    ]
    turns = [
        Turn(
            speaker=str(item["speaker"]),
            start_seconds=float(item["start_seconds"]),
            text=str(item["text"]).strip(),
        )
        for item in raw["turns"]
    ]
    turns.sort(key=lambda item: (item.start_seconds, item.speaker))
    validate_scenario(raw, participants, turns)
    return raw, participants, turns


def validate_scenario(
    raw: dict[str, Any], participants: list[Participant], turns: list[Turn]
) -> None:
    participant_ids = {participant.participant_id for participant in participants}
    participant_by_id = {
        participant.participant_id: participant for participant in participants
    }
    if not participant_ids:
        raise SystemExit("scenario has no participants")
    for turn in turns:
        if turn.speaker not in participant_ids:
            raise SystemExit(f"turn references unknown participant: {turn.speaker}")
        if turn.start_seconds < 0:
            raise SystemExit(f"turn has negative start_seconds: {turn}")
        if not turn.text:
            raise SystemExit(f"turn has empty text: {turn}")
        join_delay = participant_by_id[turn.speaker].join_delay_seconds
        if turn.start_seconds < join_delay:
            raise SystemExit(
                f"turn for {turn.speaker} starts before join_delay_seconds "
                f"({turn.start_seconds} < {join_delay})"
            )
    duration_seconds = float(raw.get("duration_seconds") or 0.0)
    if duration_seconds <= 0:
        raise SystemExit("scenario.duration_seconds must be > 0")


def ensure_tools() -> None:
    for cmd in ("ffmpeg", "ffprobe"):
        if not shutil.which(cmd):
            raise SystemExit(f"{cmd} is required")


def ensure_kokoro_assets(asset_dir: Path) -> tuple[Path, Path]:
    asset_dir.mkdir(parents=True, exist_ok=True)
    model_path = asset_dir / "kokoro-v1.0.int8.onnx"
    voices_path = asset_dir / "voices-v1.0.bin"
    download_if_missing(KOKORO_MODEL_URL, model_path)
    download_if_missing(KOKORO_VOICES_URL, voices_path)
    return model_path, voices_path


def download_if_missing(url: str, target: Path) -> None:
    if target.is_file() and target.stat().st_size > 0:
        return
    target.parent.mkdir(parents=True, exist_ok=True)
    print(f"Fetching {target.name} into {target.parent}", file=sys.stderr)
    with urllib.request.urlopen(url) as response:
        with tempfile.NamedTemporaryFile(dir=target.parent, delete=False) as tmp:
            while True:
                chunk = response.read(1024 * 1024)
                if not chunk:
                    break
                tmp.write(chunk)
            temp_path = Path(tmp.name)
    temp_path.replace(target)


def kokoro_lang(lang_code: str) -> str:
    normalized = lang_code.strip().lower()
    if normalized in {"a", "en", "en-us", "us"}:
        return "en-us"
    if normalized in {"b", "en-gb", "gb", "uk"}:
        return "en-gb"
    return normalized or "en-us"


def os_environ(name: str, default: str) -> str:
    value = os.getenv(name)
    if value:
        return value
    return default


def write_wav(path: Path, sample_rate: int, samples: np.ndarray) -> None:
    clipped = np.clip(samples, -1.0, 1.0)
    pcm = (clipped * 32767.0).astype(np.int16)
    with wave.open(str(path), "wb") as handle:
        handle.setnchannels(1)
        handle.setsampwidth(2)
        handle.setframerate(sample_rate)
        handle.writeframes(pcm.tobytes())


def run(cmd: list[str]) -> None:
    subprocess.run(cmd, check=True)


def render_video(
    prefix: Path, participant: Participant, title: str, wav_path: Path
) -> Path:
    mp4_path = prefix.with_suffix(".mp4")
    wave_primary, wave_secondary = waveform_colors(participant.theme_color)
    lines = [
        participant.display_name,
        participant.role or "Synthetic speaker",
        title,
        "Synthetic meeting fixture",
    ]
    filter_graph = (
        f"[1:a]aformat=channel_layouts=mono,showwaves=s=1120x140:mode=cline:"
        f"colors={wave_primary}|{wave_secondary}:rate=30,format=rgba[w];"
        f"[0:v]drawbox=x=56:y=56:w=1168:h=608:color=white@0.05:t=fill,"
        f"drawbox=x=56:y=56:w=1168:h=608:color=white@0.18:t=2,"
        f"drawbox=x=84:y=96:w=10:h=200:color={wave_primary}@0.95:t=fill,"
        f"drawtext=text='{escape_drawtext(lines[0])}':fontsize=52:fontcolor=white:x=112:y=96,"
        f"drawtext=text='{escape_drawtext(lines[1])}':fontsize=28:fontcolor=white@0.88:x=112:y=164,"
        f"drawtext=text='{escape_drawtext(lines[2])}':fontsize=24:fontcolor=white@0.68:x=112:y=212,"
        f"drawtext=text='{escape_drawtext(lines[3])}':fontsize=24:fontcolor=white@0.68:x=112:y=248,"
        f"drawtext=text='%{{pts\\:hms}}':fontsize=28:fontcolor=white@0.72:x=w-220:y=96[bg];"
        f"[bg]drawbox=x=80:y=520:w=1120:h=140:color=white@0.08:t=2[plate];"
        f"[plate][w]overlay=80:520[outv]"
    )
    run([
        FFMPEG_BIN,
        "-y",
        "-v",
        "error",
        "-f",
        "lavfi",
        "-i",
        "color=c=0x111827:s=1280x720:r=30",
        "-i",
        str(wav_path),
        "-filter_complex",
        filter_graph,
        "-map",
        "[outv]",
        "-map",
        "1:a:0",
        "-c:v",
        "libx264",
        "-pix_fmt",
        "yuv420p",
        "-c:a",
        "aac",
        "-b:a",
        "128k",
        "-shortest",
        str(mp4_path),
    ])
    return mp4_path


def render_publish_assets(
    prefix: Path, mp4_path: Path, wav_path: Path
) -> dict[str, str]:
    ivf_path = prefix.with_suffix(".ivf")
    ogg_path = prefix.with_suffix(".ogg")
    run([
        FFMPEG_BIN,
        "-y",
        "-v",
        "error",
        "-i",
        str(mp4_path),
        "-an",
        "-c:v",
        "libvpx",
        "-b:v",
        "1800k",
        "-deadline",
        "realtime",
        "-cpu-used",
        "5",
        "-f",
        "ivf",
        str(ivf_path),
    ])
    run([
        FFMPEG_BIN,
        "-y",
        "-v",
        "error",
        "-i",
        str(wav_path),
        "-c:a",
        "libopus",
        "-b:a",
        "96k",
        "-ac",
        "1",
        "-ar",
        "48000",
        "-f",
        "ogg",
        str(ogg_path),
    ])
    return {
        "mp4_preview": str(mp4_path),
        "video_ivf": str(ivf_path),
        "audio_ogg": str(ogg_path),
        "wav_source": str(wav_path),
    }


def waveform_colors(theme_color: str) -> tuple[str, str]:
    color = normalize_color(theme_color)
    return color, "0x93c5fd"


def normalize_color(value: str) -> str:
    stripped = value.strip().lstrip("#")
    if len(stripped) != 6:
        return "0x60a5fa"
    return "0x" + stripped.lower()


def escape_drawtext(value: str) -> str:
    return (
        value.replace("\\", "\\\\")
        .replace(":", "\\:")
        .replace("'", "\\'")
        .replace("%", "\\%")
        .replace(",", "\\,")
        .replace("[", "\\[")
        .replace("]", "\\]")
    )


def format_reference(turns: list[dict[str, Any]]) -> str:
    lines = []
    for turn in turns:
        timestamp = f"{turn['start_seconds']:.1f}s"
        lines.append(f"[{timestamp}] {turn['display_name']}: {turn['text']}")
    return "\n".join(lines) + "\n"


def main() -> None:
    args = parse_args()
    ensure_tools()

    scenario_path = Path(args.scenario).resolve()
    output_dir = Path(args.output_dir).resolve()
    manifest_path = output_dir / "manifest.json"

    if (
        not args.force
        and manifest_path.is_file()
        and cached_manifest_is_complete(manifest_path)
    ):
        print(manifest_path)
        return

    raw_scenario, participants, turns = load_scenario(scenario_path)
    output_dir.mkdir(parents=True, exist_ok=True)
    work_dir = output_dir / "_work"
    work_dir.mkdir(parents=True, exist_ok=True)

    backend: TTSBackend
    if args.backend == "mock":
        backend = MockBackend()
    else:
        backend = KokoroBackend()

    turns_by_speaker: dict[str, list[Turn]] = {
        participant.participant_id: [] for participant in participants
    }
    for turn in turns:
        turns_by_speaker[turn.speaker].append(turn)

    manifest_turns: list[dict[str, Any]] = []
    participant_entries: list[dict[str, Any]] = []
    duration_seconds = float(raw_scenario["duration_seconds"])
    sample_rate = 24_000

    synthesized: dict[tuple[str, float], tuple[np.ndarray, float]] = {}
    for participant in participants:
        for turn in turns_by_speaker[participant.participant_id]:
            turn_sr, audio = backend.synthesize(participant, turn.text)
            if turn_sr != sample_rate:
                audio = resample_linear(audio, turn_sr, sample_rate)
            synthesized[(participant.participant_id, turn.start_seconds)] = (
                audio,
                len(audio) / sample_rate,
            )

    max_end = duration_seconds
    for turn in turns:
        _, actual_duration = synthesized[(turn.speaker, turn.start_seconds)]
        max_end = max(max_end, turn.start_seconds + actual_duration + 0.6)
    total_samples = int(math.ceil(max_end * sample_rate))

    for participant in participants:
        track = np.zeros(total_samples, dtype=np.float32)
        previous_end = -1
        actual_turns: list[dict[str, Any]] = []
        for turn in turns_by_speaker[participant.participant_id]:
            audio, actual_duration = synthesized[
                (participant.participant_id, turn.start_seconds)
            ]
            start_index = int(round(turn.start_seconds * sample_rate))
            if start_index < previous_end:
                # A scenario's start times are written by hand against guessed
                # utterance lengths, so a turn can be scheduled before the same
                # speaker's previous one has finished. Aborting here fails the
                # whole run on one bad number, and — because the abort happens
                # partway through — leaves the earlier participants generated and
                # the later ones missing, which is easy to mistake for a complete
                # fixture. Slide the turn instead and say so.
                slipped = (previous_end - start_index) / sample_rate
                print(
                    f"note: {participant.participant_id} turn at {turn.start_seconds}s "
                    f"starts before their previous one ended; sliding it {slipped:.2f}s",
                    file=sys.stderr,
                )
                start_index = previous_end
            end_index = start_index + audio.size
            if end_index > track.size:
                expanded = np.zeros(end_index, dtype=np.float32)
                expanded[: track.size] = track
                track = expanded
            track[start_index:end_index] += audio
            previous_end = end_index
            actual_turns.append({
                "speaker": participant.participant_id,
                "display_name": participant.display_name,
                "start_seconds": round(turn.start_seconds, 3),
                "actual_duration_seconds": round(actual_duration, 3),
                "text": turn.text,
            })
            manifest_turns.append(actual_turns[-1])

        peak = float(np.max(np.abs(track))) if track.size else 0.0
        if peak > 0.98:
            track = track / peak * 0.96

        prefix = output_dir / participant.participant_id
        wav_path = work_dir / f"{participant.participant_id}.wav"
        write_wav(wav_path, sample_rate, track)
        mp4_path = render_video(
            prefix, participant, str(raw_scenario["title"]), wav_path
        )
        output_paths = render_publish_assets(prefix, mp4_path, wav_path)
        # Store paths relative to output_dir so the manifest is portable.
        # The streamer joins these with $OUTPUT_DIR at runtime; committing
        # absolute paths from one developer's machine would break CI and
        # every other contributor.
        rel_paths = {
            key: str(Path(value).relative_to(output_dir))
            for key, value in output_paths.items()
        }
        participant_entries.append({
            "id": participant.participant_id,
            "display_name": participant.display_name,
            "role": participant.role,
            "voice": participant.voice,
            "lang_code": participant.lang_code,
            "join_delay_seconds": participant.join_delay_seconds,
            "media_prefix": participant.participant_id,
            "turn_count": len(actual_turns),
            "paths": rel_paths,
        })

    manifest_turns.sort(key=lambda item: (item["start_seconds"], item["speaker"]))
    reference_path = output_dir / "reference.txt"
    reference_path.write_text(format_reference(manifest_turns), encoding="utf-8")

    manifest = {
        "scenario_id": raw_scenario["scenario_id"],
        "title": raw_scenario["title"],
        "description": raw_scenario.get("description", ""),
        "backend": args.backend,
        "duration_seconds": round(max_end, 3),
        # Scenario lives outside output_dir; store basename for documentation.
        "scenario_path": scenario_path.name,
        "reference_path": reference_path.relative_to(output_dir).as_posix(),
        "participants": participant_entries,
        "turns": manifest_turns,
    }
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(manifest_path)


def resample_linear(
    audio: np.ndarray, source_rate: int, target_rate: int
) -> np.ndarray:
    if source_rate == target_rate or audio.size == 0:
        return audio.astype(np.float32, copy=False)
    duration = audio.size / float(source_rate)
    target_samples = max(1, int(round(duration * target_rate)))
    source_positions = np.linspace(
        0.0, duration, audio.size, endpoint=False, dtype=np.float64
    )
    target_positions = np.linspace(
        0.0, duration, target_samples, endpoint=False, dtype=np.float64
    )
    resampled = np.interp(
        target_positions, source_positions, audio.astype(np.float64, copy=False)
    )
    return resampled.astype(np.float32)


def cached_manifest_is_complete(manifest_path: Path) -> bool:
    try:
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return False
    participants = manifest.get("participants") or []
    if not isinstance(participants, list) or not participants:
        return False
    for participant in participants:
        if not isinstance(participant, dict):
            return False
        prefix = participant.get("media_prefix")
        paths = participant.get("paths") or {}
        if not prefix or not isinstance(paths, dict):
            return False
        required = [
            f"{prefix}.ivf",
            f"{prefix}.ogg",
            f"{prefix}.mp4",
            paths.get("wav_source"),
        ]
        for raw_path in required:
            if not raw_path or not Path(raw_path).is_file():
                return False
    return True


if __name__ == "__main__":
    main()
