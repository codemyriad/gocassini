from __future__ import annotations

import argparse
from pathlib import Path

from .pipeline import build_meeting_artifact


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Build a Cassini meeting artifact from a multitrack MKV using a "
            "compatible HTTP transcription service."
        )
    )
    parser.add_argument("--input", required=True, type=Path, help="Source multitrack MKV")
    parser.add_argument("--output-dir", required=True, type=Path, help="Artifact output directory")
    parser.add_argument(
        "--transcriber-url",
        required=True,
        help="HTTP endpoint that accepts multipart file uploads and returns text + words",
    )
    parser.add_argument(
        "--audio-name",
        default="meeting.webm",
        help="Final listenable audio filename inside the artifact directory",
    )
    parser.add_argument(
        "--transcript-name",
        default="transcript.words.v1.json",
        help="Canonical transcript filename",
    )
    parser.add_argument(
        "--captions-name",
        default="captions.vtt",
        help="Derived captions filename",
    )
    parser.add_argument(
        "--manifest-name",
        default="manifest.json",
        help="Optional manifest filename",
    )
    parser.add_argument(
        "--timeline-name",
        default="timeline.map.v1.json",
        help="Optional source-to-digest timeline map filename",
    )
    parser.add_argument(
        "--timeout-seconds",
        type=int,
        default=3600,
        help="Per-track transcription timeout in seconds",
    )
    parser.add_argument(
        "--segment-gap-ms",
        type=int,
        default=900,
        help="Split segments when the gap between words exceeds this threshold",
    )
    parser.add_argument(
        "--max-segment-ms",
        type=int,
        default=15000,
        help="Split segments when they exceed this duration",
    )
    parser.add_argument(
        "--max-segment-words",
        type=int,
        default=32,
        help="Split segments when they exceed this word count",
    )
    parser.add_argument(
        "--silence-noise-db",
        type=float,
        default=-35.0,
        help="Silence detector threshold in dB",
    )
    parser.add_argument(
        "--minimum-silence-ms",
        type=int,
        default=400,
        help="Minimum silence duration used for speech activity detection",
    )
    parser.add_argument(
        "--minimum-activity-ms",
        type=int,
        default=250,
        help="Minimum retained speech activity span",
    )
    parser.add_argument(
        "--activity-padding-ms",
        type=int,
        default=200,
        help="Speech padding added around meeting-level activity before digest cuts",
    )
    parser.add_argument(
        "--keep-silence-ms",
        type=int,
        default=900,
        help="Keep silence up to this duration without compression",
    )
    parser.add_argument(
        "--compress-silence-to-ms",
        type=int,
        default=800,
        help="Compress longer all-speaker silence down to this duration",
    )
    parser.add_argument(
        "--chunk-padding-ms",
        type=int,
        default=200,
        help="Padding added around per-speaker activity before chunking",
    )
    parser.add_argument(
        "--max-chunk-ms",
        type=int,
        default=25000,
        help="Maximum transcription chunk duration",
    )
    parser.add_argument(
        "--chunk-overlap-ms",
        type=int,
        default=500,
        help="Overlap used when a chunk must be split",
    )
    parser.add_argument(
        "--max-bridge-gap-ms",
        type=int,
        default=1500,
        help="Merge nearby activity spans into one chunk when the silent gap is below this limit",
    )
    parser.add_argument(
        "--work-dir",
        type=Path,
        help="Directory for intermediate extracted tracks and raw responses",
    )
    parser.add_argument(
        "--keep-work-dir",
        action="store_true",
        help="Keep intermediates instead of using a temporary directory",
    )
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    result = build_meeting_artifact(
        input_path=args.input,
        output_dir=args.output_dir,
        transcriber_url=args.transcriber_url,
        audio_name=args.audio_name,
        transcript_name=args.transcript_name,
        captions_name=args.captions_name,
        manifest_name=args.manifest_name,
        timeline_name=args.timeline_name,
        timeout_seconds=args.timeout_seconds,
        segment_gap_ms=args.segment_gap_ms,
        max_segment_ms=args.max_segment_ms,
        max_segment_words=args.max_segment_words,
        silence_noise_db=args.silence_noise_db,
        minimum_silence_ms=args.minimum_silence_ms,
        minimum_activity_ms=args.minimum_activity_ms,
        activity_padding_ms=args.activity_padding_ms,
        keep_silence_ms=args.keep_silence_ms,
        compress_silence_to_ms=args.compress_silence_to_ms,
        chunk_padding_ms=args.chunk_padding_ms,
        max_chunk_ms=args.max_chunk_ms,
        chunk_overlap_ms=args.chunk_overlap_ms,
        max_bridge_gap_ms=args.max_bridge_gap_ms,
        work_dir=args.work_dir,
        keep_work_dir=args.keep_work_dir,
    )
    print(f"audio={result['audio_path']}")
    print(f"transcript={result['transcript_path']}")
    print(f"captions={result['captions_path']}")
    if result.get("timeline_path"):
        print(f"timeline={result['timeline_path']}")
    if result.get("manifest_path"):
        print(f"manifest={result['manifest_path']}")
    print(f"speakers={result['speaker_count']}")
    print(f"segments={result['segment_count']}")
    print(f"words={result['word_count']}")
    print(f"chunks={result['chunk_count']}")
    print(f"timeline_segments={result['timeline_segment_count']}")
    print(f"source_duration_ms={result['source_duration_ms']}")
    print(f"duration_ms={result['duration_ms']}")
    print(f"reduction_ms={result['reduction_ms']}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
