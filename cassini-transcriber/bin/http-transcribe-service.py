#!/usr/bin/env python3

from __future__ import annotations

import argparse
import tempfile
from pathlib import Path
from typing import Any

from fastapi import FastAPI, File, HTTPException, UploadFile
import uvicorn


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Expose a Cassini-compatible /v1/transcribe HTTP endpoint."
    )
    parser.add_argument(
        "--engine",
        choices=("whisper", "parakeet"),
        required=True,
        help="ASR engine to load behind the HTTP endpoint",
    )
    parser.add_argument("--model", required=True, help="Model id or local path")
    parser.add_argument(
        "--device",
        default="cuda",
        help="Device hint such as cuda, cpu, or cuda:0",
    )
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8174)
    return parser.parse_args()


class ASREngine:
    def transcribe(self, audio_path: Path) -> dict[str, Any]:
        raise NotImplementedError


class FasterWhisperEngine(ASREngine):
    def __init__(self, model: str, device: str) -> None:
        from faster_whisper import WhisperModel  # type: ignore[import-not-found]

        compute_type = "float16" if device.startswith("cuda") else "int8"
        self._model = WhisperModel(model, device=device, compute_type=compute_type)

    def transcribe(self, audio_path: Path) -> dict[str, Any]:
        segments, _ = self._model.transcribe(
            str(audio_path),
            word_timestamps=True,
            condition_on_previous_text=False,
        )
        words: list[dict[str, Any]] = []
        texts: list[str] = []
        for segment in segments:
            texts.append(segment.text.strip())
            for word in segment.words or []:
                text = str(word.word or "").strip()
                if not text:
                    continue
                entry: dict[str, Any] = {
                    "word": text,
                    "start": float(word.start or 0.0),
                    "end": float(word.end or 0.0),
                }
                if isinstance(word.probability, (float, int)):
                    entry["confidence"] = float(word.probability)
                words.append(entry)
        return {
            "text": " ".join(part for part in texts if part).strip(),
            "words": words,
        }


def _as_float(value: Any) -> float | None:
    if value is None:
        return None
    try:
        return float(value)
    except (TypeError, ValueError):
        return None


class NemoWordTimestampEngine(ASREngine):
    def __init__(self, model: str, device: str) -> None:
        import torch  # type: ignore[import-not-found]
        from nemo.collections.asr.models import ASRModel  # type: ignore[import-not-found]

        use_cuda = device.startswith("cuda") and torch.cuda.is_available()
        self._map_location = device if use_cuda else "cpu"
        self._model = ASRModel.from_pretrained(model_name=model, map_location=self._map_location)

    def transcribe(self, audio_path: Path) -> dict[str, Any]:
        outputs = self._model.transcribe(
            [str(audio_path)],
            timestamps=True,
            batch_size=1,
        )
        if not outputs:
            return {"text": "", "words": []}

        item = outputs[0]
        text = str(getattr(item, "text", "") or "").strip()
        timestamp_payload = getattr(item, "timestamp", None) or {}
        raw_chunks = timestamp_payload.get("word") or []
        words: list[dict[str, Any]] = []
        for chunk in raw_chunks:
            if not isinstance(chunk, dict):
                continue
            token_text = str(chunk.get("word") or "").strip()
            if not token_text:
                continue
            start = _as_float(chunk.get("start"))
            end = _as_float(chunk.get("end"))
            if start is None or end is None:
                continue
            words.append(
                {
                    "word": token_text,
                    "start": start,
                    "end": end,
                }
            )
        return {
            "text": text,
            "words": words,
        }


def build_engine(engine_name: str, model: str, device: str) -> ASREngine:
    if engine_name == "whisper":
        return FasterWhisperEngine(model=model, device=device)
    if engine_name == "parakeet":
        return NemoWordTimestampEngine(model=model, device=device)
    raise ValueError(f"unsupported engine: {engine_name}")


def create_app(engine: ASREngine) -> FastAPI:
    app = FastAPI(title="Cassini Transcribe Service")

    @app.get("/healthz")
    def healthz() -> dict[str, str]:
        return {"status": "ok"}

    @app.post("/v1/transcribe")
    async def transcribe(file: UploadFile = File(...)) -> dict[str, Any]:
        suffix = Path(file.filename or "audio.bin").suffix
        with tempfile.NamedTemporaryFile(delete=False, suffix=suffix) as handle:
            tmp_path = Path(handle.name)
            handle.write(await file.read())
        try:
            return engine.transcribe(tmp_path)
        except Exception as exc:  # pragma: no cover - exercised in integration
            raise HTTPException(status_code=500, detail=str(exc)) from exc
        finally:
            tmp_path.unlink(missing_ok=True)

    return app


def main() -> int:
    args = parse_args()
    engine = build_engine(engine_name=args.engine, model=args.model, device=args.device)
    app = create_app(engine)
    uvicorn.run(app, host=args.host, port=args.port, log_level="info")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
