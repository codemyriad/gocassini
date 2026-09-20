#!/usr/bin/env python3
"""CUDA-only Voxtral Realtime delay/accuracy benchmark."""

from __future__ import annotations

import argparse
import importlib.metadata
import json
import os
import sys
import tempfile
import time
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent.parent / "scripts"
sys.path.insert(0, str(SCRIPT_DIR))

from benchlib import (  # noqa: E402
    atomic_json_write,
    load_manifest,
    read_wav_info,
    select_fixture_names,
    sha256_file,
    verify_fixtures,
)
from voxtral_support import (  # noqa: E402
    ResultTelemetry,
    VOXTRAL_REALTIME_SNAPSHOT,
    environment_requests_offline,
    resolve_model_snapshot,
)


DEFAULT_MODEL_ID = VOXTRAL_REALTIME_SNAPSHOT.model_id
DEFAULT_MODEL_REVISION = VOXTRAL_REALTIME_SNAPSHOT.revision
DEFAULT_FIXTURES = (
    "2026-03-06_0742.wav",
    "2026-03-06_0961.wav",
    "mix.wav",
    "okay-mix.wav",
)


def rewrite_delay(value, delay_ms: int) -> int:
    changed = 0
    if isinstance(value, dict):
        for key, child in value.items():
            if key == "transcription_delay_ms":
                value[key] = delay_ms
                changed += 1
            else:
                changed += rewrite_delay(child, delay_ms)
    elif isinstance(value, list):
        for child in value:
            changed += rewrite_delay(child, delay_ms)
    return changed


def processor_for_delay(snapshot: Path, delay_ms: int, workspace: Path, auto_processor):
    overlay = workspace / f"delay-{delay_ms}"
    overlay.mkdir()
    for source in snapshot.iterdir():
        if source.name == "tekken.json":
            continue
        os.symlink(source.resolve(), overlay / source.name, target_is_directory=source.is_dir())
    tekken = json.loads((snapshot / "tekken.json").read_text(encoding="utf-8"))
    changed = rewrite_delay(tekken, delay_ms)
    if changed == 0:
        raise RuntimeError("tekken.json has no transcription_delay_ms field")
    (overlay / "tekken.json").write_text(json.dumps(tekken), encoding="utf-8")
    return auto_processor.from_pretrained(overlay, local_files_only=True), changed


def transcribe(processor, model, path: Path, delay_ms: int, max_new_tokens: int | None) -> dict:
    import torch
    from mistral_common.tokens.tokenizers.audio import Audio

    audio = Audio.from_file(str(path), strict=False)
    audio.resample(processor.feature_extractor.sampling_rate)
    inputs = processor(audio.audio_array, return_tensors="pt")
    inputs = inputs.to(model.device, dtype=model.dtype)
    torch.cuda.synchronize()
    started = time.perf_counter()
    generate_options = {} if max_new_tokens is None else {"max_new_tokens": max_new_tokens}
    with torch.inference_mode():
        outputs = model.generate(**inputs, **generate_options)
    torch.cuda.synchronize()
    elapsed = time.perf_counter() - started
    text = processor.batch_decode(outputs, skip_special_tokens=True)[0].strip()
    seconds = read_wav_info(path).duration_seconds
    return {
        "kind": "transcription",
        "label": f"delay-{delay_ms}-{path.name}",
        "audio": path.name,
        "delayMs": delay_ms,
        "audioSeconds": seconds,
        "elapsedSeconds": elapsed,
        "rtf": elapsed / seconds,
        "text": text,
    }


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--audio-dir", type=Path, required=True)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--model-id", default=DEFAULT_MODEL_ID)
    parser.add_argument("--revision", default=DEFAULT_MODEL_REVISION)
    parser.add_argument(
        "--snapshot-dir",
        type=Path,
        help="use this already-downloaded model directory without contacting Hugging Face",
    )
    parser.add_argument(
        "--model-cache-dir",
        type=Path,
        help="optional Hugging Face snapshot cache (HF_HOME remains supported)",
    )
    parser.add_argument(
        "--local-files-only",
        action="store_true",
        help="require the model revision to be present locally; never download it",
    )
    parser.add_argument(
        "--telemetry-dir",
        type=Path,
        help="first-result/progress sidecar directory (default: OUTPUT parent/.telemetry)",
    )
    parser.add_argument("--delays", default="480,2400")
    parser.add_argument("--fixtures", default=",".join(DEFAULT_FIXTURES))
    parser.add_argument("--max-new-tokens", type=int)
    args = parser.parse_args()
    if args.max_new_tokens is not None and not 16 <= args.max_new_tokens <= 1024:
        raise SystemExit("--max-new-tokens must be in 16..1024")

    os.environ.setdefault("TOKENIZERS_PARALLELISM", "false")
    os.environ.setdefault("OMP_NUM_THREADS", "1")
    os.environ.setdefault("MKL_NUM_THREADS", "1")

    import torch
    from transformers import AutoProcessor, VoxtralRealtimeForConditionalGeneration

    torch.set_num_threads(1)
    torch.set_num_interop_threads(1)
    if not torch.cuda.is_available():
        raise SystemExit("CUDA unavailable; refusing CPU inference")
    if not torch.cuda.is_bf16_supported():
        raise SystemExit("BF16 unavailable; refusing an unplanned fallback")

    verify_fixtures(args.manifest, args.audio_dir)
    manifest = load_manifest(args.manifest)
    selected = select_fixture_names(manifest, args.fixtures)
    audio_paths = [args.audio_dir / name for name in selected]
    try:
        delays = [int(item.strip()) for item in args.delays.split(",") if item.strip()]
    except ValueError as error:
        raise SystemExit(f"invalid --delays: {error}") from error
    allowed = {*range(80, 1201, 80), 2400}
    if not delays or len(delays) != len(set(delays)) or any(delay not in allowed for delay in delays):
        raise SystemExit("delays must be unique values in 80..1200 (80 ms steps), or 2400")

    telemetry = ResultTelemetry(
        args.output,
        runner="voxtral-realtime",
        model_id=args.model_id,
        revision=args.revision,
        expected_results=len(delays) * len(audio_paths),
        telemetry_dir=args.telemetry_dir,
    )
    snapshot = resolve_model_snapshot(
        args.model_id,
        args.revision,
        VOXTRAL_REALTIME_SNAPSHOT,
        snapshot_dir=args.snapshot_dir,
        cache_dir=args.model_cache_dir,
        local_files_only=args.local_files_only or environment_requests_offline(),
    )
    load_started = time.perf_counter()
    model = VoxtralRealtimeForConditionalGeneration.from_pretrained(
        snapshot,
        device_map="cuda",
        dtype=torch.bfloat16,
        low_cpu_mem_usage=True,
        local_files_only=True,
    )
    model.eval()
    if model.device.type != "cuda":
        raise SystemExit(f"model device is {model.device}; refusing CPU inference")
    torch.cuda.synchronize()
    load_seconds = time.perf_counter() - load_started

    results: list[dict] = []
    delay_metadata: dict[str, dict] = {}
    with tempfile.TemporaryDirectory(prefix="voxtral-realtime-") as temporary:
        workspace = Path(temporary)
        for delay in delays:
            processor, changed = processor_for_delay(snapshot, delay, workspace, AutoProcessor)
            delay_metadata[str(delay)] = {"tekkenFieldsChanged": changed}
            for path in audio_paths:
                result = transcribe(processor, model, path, delay, args.max_new_tokens)
                results.append(result)
                telemetry.record(result)

    payload = {
        "schema": "gocassini.voxtral-realtime-benchmark.v2",
        "fixtureSet": manifest["fixtureSet"],
        "fixtureManifestSha256": sha256_file(args.manifest),
        "model": args.model_id,
        "modelRevision": args.revision,
        "device": torch.cuda.get_device_name(0),
        "cuda": torch.version.cuda,
        "torch": torch.__version__,
        "transformers": importlib.metadata.version("transformers"),
        "loadSeconds": load_seconds,
        "peakAllocatedMiB": torch.cuda.max_memory_allocated() / 1024**2,
        "peakReservedMiB": torch.cuda.max_memory_reserved() / 1024**2,
        "delayMetadata": delay_metadata,
        "results": results,
    }
    atomic_json_write(args.output, payload)
    telemetry.complete()
    print(args.output)


if __name__ == "__main__":
    main()
