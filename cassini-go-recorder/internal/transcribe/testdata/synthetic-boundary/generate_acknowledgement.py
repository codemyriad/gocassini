#!/usr/bin/env python3
"""Regenerate the synthetic acknowledgement counterexample, without private audio."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile
import wave

import numpy as np
import onnxruntime as ort
from kokoro_onnx import Kokoro


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--assets', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise SystemExit('Refusing to overwrite existing audio')
    manifest = json.loads(Path(__file__).with_name('manifest.json').read_text())
    for name, digest in manifest['synthesis']['assets'].items():
        if hashlib.sha256((args.assets / name).read_bytes()).hexdigest() != digest:
            raise SystemExit(f'Unexpected synthesis asset: {name}')
    options = ort.SessionOptions()
    options.intra_op_num_threads = 2
    options.inter_op_num_threads = 1
    options.execution_mode = ort.ExecutionMode.ORT_SEQUENTIAL
    session = ort.InferenceSession(str(args.assets / 'kokoro-v1.0.int8.onnx'),
                                   sess_options=options, providers=['CPUExecutionProvider'])
    engine = Kokoro.from_session(session, str(args.assets / 'voices-v1.0.bin'))
    audio, rate = engine.create('Mm-hmm, right, that makes sense.', voice='am_adam',
                               speed=1.0, lang='en-us')
    audio = np.asarray(audio, dtype=np.float32).reshape(-1)
    peak = float(np.max(np.abs(audio)))
    if peak > 0.98:
        audio = audio / peak * 0.96
    audio *= 10.0 ** (-6.0 / 20.0)
    with tempfile.TemporaryDirectory() as tmp:
        raw = Path(tmp) / 'raw.wav'
        with wave.open(str(raw), 'wb') as out:
            out.setparams((1, 2, rate, 0, 'NONE', 'not compressed'))
            out.writeframes((np.clip(audio, -1, 1) * 32767).astype('<i2').tobytes())
        subprocess.run(['ffmpeg', '-v', 'error', '-i', str(raw), '-ar', '16000',
                        '-ac', '1', '-c:a', 'pcm_s16le', str(args.output)], check=True)
    print(hashlib.sha256(args.output.read_bytes()).hexdigest())


if __name__ == '__main__':
    main()
