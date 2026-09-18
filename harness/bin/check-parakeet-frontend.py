#!/usr/bin/env python3
"""Opt-in Parakeet TDT frontend research diagnostic; NOT a production replacement.

Requires numpy, kaldi-native-fbank==1.22.3, and onnxruntime in a separate Python
research environment. Installs/downloads nothing. Uses local FP32 model files.

Example:
  python check-parakeet-frontend.py --model-dir /private/model \
    --audio /private/mono-16k.wav --output /private/results.jsonl

Compares Sherpa-style NeMo preprocessing with model-reference settings using
identical ONNX weights and a greedy TDT decoder. Neither condition uses hotwords,
VAD, chunk splitting, energy filtering, or production beam search. The output
contains private recognized text: keep it outside the repository/public reports.
Reference settings follow NVIDIA Parakeet v3's cached .nemo configuration and
NeMo AudioToMelSpectrogramPreprocessor: symmetric Hann, centered zero padding,
Nyquist upper frequency, additive 2**-24 log guard, sample variance. KNF's raw
mel computation numerically matched published Transformers features to ~1e-6
RMSE on investigated clips. Normalization uses stable centered variance in both conditions; native runtime
versions with a different variance implementation can differ. This does not
validate a production implementation.
"""
import argparse
import hashlib
import json
from pathlib import Path
import time
import wave


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument('--model-dir', type=Path, required=True)
    p.add_argument('--audio', type=Path, action='append', required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--head-ms', type=int, default=0)
    p.add_argument('--tail-ms', type=int, action='append', help='repeatable; defaults to 0 and 500')
    p.add_argument('--threads', type=int, default=4)
    args = p.parse_args()
    tails = args.tail_ms if args.tail_ms is not None else [0, 500]
    if args.head_ms < 0 or min(tails) < 0 or args.threads < 1:
        p.error('padding must be nonnegative and threads positive')
    import numpy as np
    import kaldi_native_fbank as knf
    import onnxruntime as ort

    opts = ort.SessionOptions()
    opts.intra_op_num_threads = args.threads
    opts.inter_op_num_threads = 1
    sessions = {name: ort.InferenceSession(str(args.model_dir / (name + '.onnx')),
                sess_options=opts, providers=['CPUExecutionProvider'])
                for name in ['encoder', 'decoder', 'joiner']}
    meta = sessions['encoder'].get_modelmeta().custom_metadata_map
    if (not meta.get('url', '').rstrip('/').endswith('/parakeet-tdt-0.6b-v3')
            or meta.get('feat_dim') != '128'
            or meta.get('normalize_type') != 'per_feature'):
        p.error('requires the 128-bin Parakeet TDT 0.6B v3 export with per_feature normalization')
    bins = int(meta['feat_dim'])
    blank = int(meta['vocab_size'])
    layers, hidden = int(meta['pred_rnn_layers']), int(meta['pred_hidden'])
    tokens = {int(line.rsplit(' ', 1)[1]): line.rsplit(' ', 1)[0]
              for line in (args.model_dir / 'tokens.txt').read_text().splitlines()}

    def features(audio, reference):
        o = knf.FbankOptions()
        o.frame_opts.dither = 0
        o.frame_opts.remove_dc_offset = False
        o.frame_opts.snip_edges = reference
        o.mel_opts.num_bins = bins
        o.mel_opts.low_freq = 0
        o.mel_opts.high_freq = 0 if reference else -400
        o.mel_opts.is_librosa = True
        x = audio
        if reference:
            o.frame_opts.window_type = 'hanning'
            o.frame_opts.preemph_coeff = 0
            o.use_log_fbank = False
            # Whole-waveform preemphasis, then centered 400-sample windows.
            x = np.concatenate([audio[:1], audio[1:] - .97 * audio[:-1]])
            x = np.pad(x, (200, 200))
        fbank = knf.OnlineFbank(o)
        fbank.accept_waveform(16000, x.tolist())
        fbank.input_finished()
        f = np.stack([fbank.get_frame(i) for i in range(fbank.num_frames_ready)])
        if reference:
            f = np.log(f[:len(audio) // 160] + 2**-24)
        return ((f - f.mean(axis=0)) / (f.std(axis=0, ddof=int(reference)) + 1e-5)).astype(np.float32)

    def run(name, values):
        s = sessions[name]
        return s.run(None, dict(zip([v.name for v in s.get_inputs()], values)))

    def decode(f):
        enc, length = run('encoder', [f.T[None], np.array([len(f)], np.int64)])
        state = [np.zeros((layers, 1, hidden), np.float32) for _ in range(2)]
        def prediction(token, states):
            return run('decoder', [np.array([[token]], np.int32), np.array([1], np.int32)] + states)
        dec = prediction(blank, state)
        ids, times, durations = [], [], []
        frame = emitted = steps = 0
        while frame < int(length[0]):
            steps += 1
            if steps > int(length[0]) * 6 + 10:
                raise RuntimeError('TDT decoder failed to advance')
            logits = run('joiner', [enc[:, :, frame:frame + 1], dec[0]])[0].reshape(-1)
            if len(logits) != blank + 1 + 5:
                raise ValueError('diagnostic expects TDT durations [0,1,2,3,4]')
            token = int(logits[:blank + 1].argmax())
            skip = int(logits[blank + 1:].argmax())
            if token != blank:
                ids.append(token); times.append(frame); durations.append(skip)
                dec = prediction(token, dec[2:])
                emitted += 1
            if skip > 0:
                emitted = 0
            if emitted >= 5 or (token == blank and skip == 0):
                emitted = 0
                skip = 1
            frame += skip
        return {'text': ''.join(tokens[i] for i in ids).replace('▁', ' ').strip(),
                'tokenIds': ids, 'encoderFrameTimes': times, 'tokenDurationsFrames': durations}

    # Exclusive create avoids overwriting a previous experiment.
    with args.output.open('x') as out:
        for path in args.audio:
            with wave.open(str(path)) as wav:
                if (wav.getnchannels(), wav.getsampwidth(), wav.getframerate()) != (1, 2, 16000):
                    raise ValueError(f'{path}: requires mono 16 kHz signed 16-bit WAV')
                pcm = np.frombuffer(wav.readframes(wav.getnframes()), dtype='<i2').astype(np.float32) / 32768
            if len(pcm) < 400:
                raise ValueError('clips shorter than one 25ms frame are unsupported')
            for tail in tails:
                audio = np.pad(pcm, (args.head_ms * 16, tail * 16))
                fs = [features(audio, False), features(audio, True)]
                n = min(map(len, fs))
                rmse = float(np.sqrt(((fs[0][:n] - fs[1][:n]) ** 2).mean()))
                for name, f in zip(['sherpa-style', 'model-reference'], fs):
                    start = time.monotonic()
                    result = decode(f)
                    result.update(audioPath=str(path), pcmSha256=hashlib.sha256(pcm.tobytes()).hexdigest(),
                        frontend=name, headMs=args.head_ms, tailMs=tail, frames=len(f),
                        frontendRMSE=rmse, seconds=time.monotonic() - start,
                        runtime=ort.__version__, modelMetadata=meta, modelDir=str(args.model_dir),
                        decoder='independent greedy TDT CPU FP32; no hotwords or VAD')
                    out.write(json.dumps(result) + '\n'); out.flush()
                    print(f'{path.name}: {name}, head={args.head_ms}, tail={tail}, {len(result["tokenIds"])} tokens')


if __name__ == '__main__':
    main()
