from __future__ import annotations

import argparse
import os
from pathlib import Path

from .pipeline import build_meeting_artifact


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Build a Cassini meeting artifact from a multitrack MKV using a "
            "compatible HTTP service or a local runtime."
        )
    )
    parser.add_argument("--input", required=True, type=Path, help="Source multitrack MKV")
    parser.add_argument("--output-dir", required=True, type=Path, help="Artifact output directory")
    parser.add_argument(
        "--transcriber-backend",
        choices=("auto", "http", "local-whisper"),
        default=os.getenv("CASSINI_TRANSCRIBER_BACKEND", "auto"),
        help="Transcription backend to use",
    )
    parser.add_argument(
        "--transcriber-url",
        default=os.getenv("CASSINI_TRANSCRIBER_URL"),
        help="HTTP endpoint that accepts multipart file uploads and returns text + words",
    )
    parser.add_argument(
        "--whisper-model",
        default=os.getenv("CASSINI_WHISPER_MODEL", "auto"),
        help="Local Whisper model name. 'auto' picks the highest-quality built-in default.",
    )
    parser.add_argument(
        "--whisper-device",
        default=os.getenv("CASSINI_WHISPER_DEVICE", "auto"),
        help="Execution device for the local Whisper backend: auto, cuda, or cpu",
    )
    parser.add_argument(
        "--whisper-download-root",
        type=Path,
        default=Path(os.getenv("CASSINI_WHISPER_DOWNLOAD_ROOT"))
        if os.getenv("CASSINI_WHISPER_DOWNLOAD_ROOT")
        else None,
        help="Optional cache directory for downloaded Whisper models",
    )
    parser.add_argument(
        "--whisper-language",
        default=os.getenv("CASSINI_WHISPER_LANGUAGE"),
        help="Optional forced Whisper language code such as 'en'",
    )
    parser.add_argument(
        "--readable-backend",
        choices=(
            "auto",
            "none",
            "openwebui",
            "openai-compatible",
            "local-transformers",
            "local-ollama",
        ),
        default=os.getenv("CASSINI_READABLE_BACKEND", "auto"),
        help="Readable transcript cleanup backend",
    )
    parser.add_argument(
        "--api-base-url",
        default=(
            os.getenv("CASSINI_API_BASE_URL")
            or os.getenv("OPENAI_BASE_URL")
            or ("https://openrouter.ai/api/v1" if os.getenv("OPENROUTER_API_KEY") else None)
        ),
        help="Base URL for an OpenAI-compatible readable transcript API",
    )
    parser.add_argument(
        "--api-key",
        default=(
            os.getenv("CASSINI_API_KEY")
            or os.getenv("OPENROUTER_API_KEY")
            or os.getenv("OPENAI_API_KEY")
        ),
        help="API key for an OpenAI-compatible readable transcript API",
    )
    parser.add_argument(
        "--api-model",
        default=(
            os.getenv("CASSINI_API_MODEL")
            or os.getenv("OPENROUTER_MODEL")
            or os.getenv("OPENAI_MODEL")
            or "openai/gpt-4o-mini"
        ),
        help="Model name for an OpenAI-compatible readable transcript API",
    )
    parser.add_argument(
        "--api-timeout-seconds",
        type=int,
        default=int(os.getenv("CASSINI_API_TIMEOUT_SECONDS", "240")),
        help="Per-request timeout for an OpenAI-compatible readable transcript API",
    )
    parser.add_argument(
        "--api-app-name",
        default=os.getenv("CASSINI_API_APP_NAME", "gocassini"),
        help="Application name sent to an OpenAI-compatible readable transcript API",
    )
    parser.add_argument(
        "--api-site-url",
        default=os.getenv("CASSINI_API_SITE_URL"),
        help="Optional site URL sent to an OpenAI-compatible readable transcript API",
    )
    parser.add_argument(
        "--local-llm-model",
        default=os.getenv("CASSINI_LOCAL_LLM_MODEL", "Qwen/Qwen2.5-1.5B-Instruct"),
        help="Local Hugging Face model used for readable transcript cleanup",
    )
    parser.add_argument(
        "--local-llm-device",
        default=os.getenv("CASSINI_LOCAL_LLM_DEVICE", "cuda"),
        help="Torch device for the local readable transcript backend",
    )
    parser.add_argument(
        "--local-llm-download-root",
        type=Path,
        default=Path(os.getenv("CASSINI_LOCAL_LLM_DOWNLOAD_ROOT"))
        if os.getenv("CASSINI_LOCAL_LLM_DOWNLOAD_ROOT")
        else None,
        help="Optional cache directory for downloaded local LLM models",
    )
    parser.add_argument(
        "--local-llm-max-new-tokens",
        type=int,
        default=int(os.getenv("CASSINI_LOCAL_LLM_MAX_NEW_TOKENS", "1024")),
        help="Maximum tokens generated per local readable transcript batch",
    )
    parser.add_argument(
        "--ollama-model",
        default=os.getenv("CASSINI_OLLAMA_MODEL", "qwen35-9b-q4:latest"),
        help="Local Ollama model used for readable transcript cleanup",
    )
    parser.add_argument(
        "--ollama-binary",
        default=os.getenv("CASSINI_OLLAMA_BINARY", "ollama"),
        help="Ollama executable to invoke for local readable transcript cleanup",
    )
    parser.add_argument(
        "--ollama-no-auto-pull",
        action="store_true",
        help="Do not auto-download the Ollama model when it is missing",
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
        "--readable-transcript-name",
        default=os.getenv("CASSINI_READABLE_TRANSCRIPT_NAME"),
        help=(
            "Optional cleaned readable transcript filename. Disabled when "
            "--readable-backend=none."
        ),
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
        "--openwebui-base-url",
        default=os.getenv("CASSINI_OPENWEBUI_BASE_URL"),
        help="Open WebUI base URL used for readable transcript generation",
    )
    parser.add_argument(
        "--openwebui-email",
        default=os.getenv("CASSINI_OPENWEBUI_EMAIL"),
        help="Open WebUI login email used for readable transcript generation",
    )
    parser.add_argument(
        "--openwebui-password",
        default=os.getenv("CASSINI_OPENWEBUI_PASSWORD"),
        help="Open WebUI login password used for readable transcript generation",
    )
    parser.add_argument(
        "--openwebui-model",
        default=os.getenv("CASSINI_OPENWEBUI_MODEL"),
        help="Open WebUI model id used for readable transcript generation",
    )
    parser.add_argument(
        "--openwebui-timeout-seconds",
        type=int,
        default=int(os.getenv("CASSINI_OPENWEBUI_TIMEOUT_SECONDS", "240")),
        help="Per-request timeout for readable transcript generation",
    )
    parser.add_argument(
        "--readable-max-gap-ms",
        type=int,
        default=int(os.getenv("CASSINI_READABLE_MAX_GAP_MS", "1800")),
        help="Maximum inter-segment gap when grouping readable transcript windows",
    )
    parser.add_argument(
        "--readable-max-window-ms",
        type=int,
        default=int(os.getenv("CASSINI_READABLE_MAX_WINDOW_MS", "45000")),
        help="Maximum readable transcript window duration",
    )
    parser.add_argument(
        "--readable-max-window-words",
        type=int,
        default=int(os.getenv("CASSINI_READABLE_MAX_WINDOW_WORDS", "120")),
        help="Maximum readable transcript window word count",
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
    if args.transcriber_backend == "http" and not args.transcriber_url:
        raise SystemExit("--transcriber-url is required when --transcriber-backend=http")
    if args.readable_backend == "none":
        args.readable_transcript_name = None
    result = build_meeting_artifact(
        input_path=args.input,
        output_dir=args.output_dir,
        transcriber_backend=args.transcriber_backend,
        transcriber_url=args.transcriber_url,
        whisper_model=args.whisper_model,
        whisper_device=args.whisper_device,
        whisper_download_root=args.whisper_download_root,
        whisper_language=args.whisper_language,
        audio_name=args.audio_name,
        transcript_name=args.transcript_name,
        readable_transcript_name=args.readable_transcript_name,
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
        readable_backend=args.readable_backend,
        openwebui_base_url=args.openwebui_base_url,
        openwebui_email=args.openwebui_email,
        openwebui_password=args.openwebui_password,
        openwebui_model=args.openwebui_model,
        openwebui_timeout_seconds=args.openwebui_timeout_seconds,
        api_base_url=args.api_base_url,
        api_key=args.api_key,
        api_model=args.api_model,
        api_timeout_seconds=args.api_timeout_seconds,
        api_app_name=args.api_app_name,
        api_site_url=args.api_site_url,
        local_llm_model=args.local_llm_model,
        local_llm_device=args.local_llm_device,
        local_llm_download_root=args.local_llm_download_root,
        local_llm_max_new_tokens=args.local_llm_max_new_tokens,
        ollama_model=args.ollama_model,
        ollama_binary=args.ollama_binary,
        ollama_auto_pull=not args.ollama_no_auto_pull,
        readable_max_gap_ms=args.readable_max_gap_ms,
        readable_max_window_ms=args.readable_max_window_ms,
        readable_max_window_words=args.readable_max_window_words,
        work_dir=args.work_dir,
        keep_work_dir=args.keep_work_dir,
    )
    print(f"audio={result['audio_path']}")
    print(f"transcript={result['transcript_path']}")
    if result.get("readable_transcript_path"):
        print(f"readable_transcript={result['readable_transcript_path']}")
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
