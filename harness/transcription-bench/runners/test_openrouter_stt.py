import io
import struct
import tempfile
import unittest
import wave
from pathlib import Path
from benchlib import read_wav_info, iter_result_cases
from openrouter_stt import canonical_wav, summarize, SCHEMA

class OpenRouterTests(unittest.TestCase):
    def test_streaming_header_preserves_pcm_and_duration(self):
        pcm = b'\x01\x00' * 16000
        data = io.BytesIO()
        with wave.open(data, 'wb') as w:
            w.setnchannels(1); w.setsampwidth(2); w.setframerate(16000); w.writeframes(pcm)
        raw = bytearray(data.getvalue())
        struct.pack_into('<I', raw, 4, 0x7fffffff)
        struct.pack_into('<I', raw, 40, 0x7fffffff)
        with tempfile.TemporaryDirectory() as d:
            p = Path(d)/'stream.wav'; p.write_bytes(raw)
            fixed = canonical_wav(p, read_wav_info(p))
        with wave.open(io.BytesIO(fixed)) as w:
            self.assertEqual(w.getnframes(), 16000)
            self.assertEqual(w.readframes(16000), pcm)

    def test_warmup_cost_counts_but_timing_does_not(self):
        runs = [dict(warmup=True, status=200, audioSeconds=10, seconds=20, usage={'cost':.01}),
                dict(warmup=False, status=200, audioSeconds=10, seconds=1, usage={'cost':.01})]
        result = summarize(runs)
        self.assertEqual(result['rtMultiplier'], 10)
        self.assertEqual(result['reportedCostUSD'], .02)
        self.assertEqual(len(list(iter_result_cases({'schema':SCHEMA, 'runs':runs,
            'results':[{'audio':'clip.wav','text':'hello'}]}))), 1)
