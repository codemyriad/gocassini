#!/usr/bin/env python3
"""Regenerate invented speech with stock Kokoro voices; no meeting recordings.

Run with the pinned requirements beside this file. The checked-in MKV is the
canonical regression input: synthesis/codec versions can change regenerated
bytes, so inspect and revalidate both old/new decodes before replacing it.
"""
import argparse
import hashlib
import importlib.metadata
import json
from pathlib import Path
import subprocess
import tempfile
import wave

import numpy as np
import onnxruntime as ort

ASSETS = {
    'kokoro-v1.0.int8.onnx': '6e742170d309016e5891a994e1ce1559c702a2ccd0075e67ef7157974f6406cb',
    'voices-v1.0.bin': 'bca610b8308e8d99f32e6fe4197e7ec01679264efed0cac9140fe9c29f1fbf7d',
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--assets', type=Path, required=True, help='directory with the two cached Kokoro assets')
    parser.add_argument('--output', type=Path, required=True, help='new output directory; refuses to overwrite fixtures')
    args = parser.parse_args()
    for name, digest in ASSETS.items():
        if hashlib.sha256((args.assets / name).read_bytes()).hexdigest() != digest:
            raise SystemExit(f'Unexpected synthesis asset: {name}')
    args.output.mkdir(parents=True, exist_ok=False)
    manifest = json.loads(Path(__file__).with_name('manifest.json').read_text())
    # Limit synthesis to CPU. Do not compete with the recognition GPU job.
    session = ort.InferenceSession

    def cpu_session(*positional, **kwargs):
        options = ort.SessionOptions()
        options.intra_op_num_threads = 2
        options.inter_op_num_threads = 1
        kwargs.update(sess_options=options, providers=['CPUExecutionProvider'])
        return session(*positional, **kwargs)

    ort.InferenceSession = cpu_session
    from kokoro_onnx import Kokoro
    engine = Kokoro(str(args.assets / 'kokoro-v1.0.int8.onnx'), str(args.assets / 'voices-v1.0.bin'))
    with tempfile.TemporaryDirectory() as tmp:
        raw = Path(tmp) / 'raw.wav'
        for i, track in enumerate(manifest['tracks']):
            audio, rate = engine.create(track['script'], voice=track['voice'], speed=1.0, lang=track['language'])
            with wave.open(str(raw), 'wb') as out:
                out.setparams((1, 2, rate, 0, 'NONE', 'not compressed'))
                out.writeframes((np.clip(audio, -1, 1) * 32767).astype('<i2').tobytes())
            subprocess.run(['ffmpeg', '-v', 'error', '-y', '-i', str(raw), '-af',
                            'adelay=700|700,apad=pad_dur=0.7', '-ar', '16000', '-ac', '1',
                            str(args.output / f'track-{i}.wav')], check=True)
    subprocess.run(['ffmpeg', '-v', 'error', '-i', str(args.output / 'track-0.wav'),
                    '-itsoffset', '3', '-i', str(args.output / 'track-1.wav'),
                    '-map', '0:a', '-map', '1:a', '-c:a', 'libopus', '-b:a', '48k',
                    '-metadata:s:a:0', 'title=Speaker-A', '-metadata:s:a:1', 'title=Speaker-B',
                    str(args.output / 'garden.mkv')], check=True)
    metadata = {
        'packages': {name: importlib.metadata.version(name) for name in
                     ['kokoro-onnx', 'numpy', 'onnxruntime', 'phonemizer-fork', 'espeakng-loader']},
        'ffmpeg': subprocess.check_output(['ffmpeg', '-version'], text=True).splitlines()[0],
        'assets': ASSETS,
        'sha256': {p.name: hashlib.sha256(p.read_bytes()).hexdigest()
                   for p in args.output.iterdir() if p.is_file()},
    }
    (args.output / 'generation.json').write_text(json.dumps(metadata, indent=2) + '\n')


if __name__ == '__main__':
    main()
