from __future__ import annotations

import argparse
import json
import os
from pathlib import Path

from .llm import build_readable_transcript_payload
from .readable import create_readable_client, load_transcript_payload, resolve_readable_backend


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Build a transcript.readable.v1.json file from an existing "
            "transcript.words.v1.json transcript."
        )
    )
    parser.add_argument(
        "--input-transcript",
        required=True,
        type=Path,
        help="Canonical transcript.words.v1.json input path",
    )
    parser.add_argument(
        "--output",
        type=Path,
        help="Readable transcript output path (default: sibling transcript.readable.v1.json)",
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
    return parser.parse_args(argv)


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    input_transcript_path = args.input_transcript.resolve()
    output_path = (
        args.output.resolve()
        if args.output is not None
        else input_transcript_path.with_name("transcript.readable.v1.json")
    )

    transcript_payload = load_transcript_payload(input_transcript_path)
    resolved_backend = resolve_readable_backend(
        readable_backend=args.readable_backend,
        readable_transcript_name=output_path.name,
        api_base_url=args.api_base_url,
        api_key=args.api_key,
        api_model=args.api_model,
        openwebui_base_url=args.openwebui_base_url,
        openwebui_email=args.openwebui_email,
        openwebui_password=args.openwebui_password,
        openwebui_model=args.openwebui_model,
        ollama_binary=args.ollama_binary,
    )
    client = create_readable_client(
        readable_backend=resolved_backend,
        api_base_url=args.api_base_url,
        api_key=args.api_key,
        api_model=args.api_model,
        api_timeout_seconds=args.api_timeout_seconds,
        api_app_name=args.api_app_name,
        api_site_url=args.api_site_url,
        openwebui_base_url=args.openwebui_base_url,
        openwebui_email=args.openwebui_email,
        openwebui_password=args.openwebui_password,
        openwebui_model=args.openwebui_model,
        openwebui_timeout_seconds=args.openwebui_timeout_seconds,
        local_llm_model=args.local_llm_model,
        local_llm_device=args.local_llm_device,
        local_llm_download_root=args.local_llm_download_root,
        local_llm_max_new_tokens=args.local_llm_max_new_tokens,
        ollama_model=args.ollama_model,
        ollama_binary=args.ollama_binary,
        ollama_auto_pull=not args.ollama_no_auto_pull,
        allow_none_passthrough=True,
    )
    readable_payload = build_readable_transcript_payload(
        transcript_payload=transcript_payload,
        client=client,
        max_gap_ms=args.readable_max_gap_ms,
        max_window_ms=args.readable_max_window_ms,
        max_window_words=args.readable_max_window_words,
    )

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(
        json.dumps(readable_payload, indent=2, ensure_ascii=False) + "\n",
        encoding="utf-8",
    )
    print(f"input_transcript={input_transcript_path}")
    print(f"output={output_path}")
    print(f"backend={resolved_backend}")
    print(f"segments={len(readable_payload.get('segments') or [])}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
