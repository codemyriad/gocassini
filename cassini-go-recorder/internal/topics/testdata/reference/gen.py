"""Regenerate the topics package's reference data from the PyTorch model.

Writes, into the directory given as the only argument:
  tokenizer_parity.json  Hugging Face tokenizer ids for public and synthetic text
  golden_vectors.json    fp32 vectors the int8 Go runtime is compared with
  probe_golden.json      the four pinned probe texts, embedded into the binary

Every text comes from texts.py: UDHR Article 1 translations and synthetic
sentences. Never add meeting text: the repository is public.

Needs sentence-transformers, torch and google/embeddinggemma-2 at the pinned
revision in the local Hugging Face cache, e.g.
  HF_HUB_OFFLINE=1 python -I gen.py /tmp/out
then copy tokenizer_parity.json and golden_vectors.json to ../ and
probe_golden.json to ../../.
"""
import json
import os
import sys
from pathlib import Path

here = Path(__file__).resolve().parent
sys.path.insert(0, str(here))
os.environ.setdefault("HF_HUB_OFFLINE", "1")
os.environ["TOKENIZERS_PARALLELISM"] = "false"

import numpy as np
import sentence_transformers
import tokenizers
import torch
import transformers
from sentence_transformers import SentenceTransformer

from texts import EDGE, PROBE, SYNTHETIC_SPEECH, UDHR, long_text

MODEL, REVISION = "google/embeddinggemma-2", "914f7f89142e33e77833254d9c9b90c3cef7303b"
# The catalogue model the probe vectors are for, and its source_sha256.
CATALOGUE_ID = "embeddinggemma-2-text-int8"
CATALOGUE_REVISION = "89cef0454bc6e723f9f7fc53df0e2c881ba672fd00d8678c02d677f0b1ebee2c"

torch.set_num_threads(8)
model = SentenceTransformer(MODEL, revision=REVISION, device="cpu", local_files_only=True,
                            config_kwargs={"vision_config": None, "audio_config": None},
                            model_kwargs={"dtype": torch.float32})
prompt = model.prompts["Clustering"]
out = Path(sys.argv[1])
generator = {"tokenizers": tokenizers.__version__, "transformers": transformers.__version__,
             "sentence_transformers": sentence_transformers.__version__, "torch": torch.__version__,
             "model": MODEL + "@" + REVISION}


def ids(text):
    return model.tokenizer(prompt + text)["input_ids"]


def vectors(texts):
    v = model.encode(texts, prompt_name="Clustering", batch_size=1, normalize_embeddings=True)
    assert np.isfinite(v).all()
    return v.astype(np.float32)


def write_vectors(path, header, texts):
    # float32 shortest repr: exact when read back as float32, and compact.
    rows = ",\n".join("[" + ",".join(str(x) for x in row) + "]" for row in vectors(texts))
    doc = {**header, "generator": generator, "prompt": prompt, "texts": texts, "ids": [ids(t) for t in texts]}
    body = json.dumps(doc, ensure_ascii=False)[:-1] + ',\n"vectors": [\n' + rows + "\n]}\n"
    json.loads(body)
    path.write_text(body)


cases = ([("udhr-" + k, v) for k, v in UDHR.items()] +
         [("speech-%d" % i, t) for i, t in enumerate(SYNTHETIC_SPEECH)] +
         [("edge-%02d" % i, t) for i, t in enumerate(EDGE)] + [("long", long_text())])
lines = ["{", ' "generator": ' + json.dumps(generator) + ",", ' "prompt": ' + json.dumps(prompt) + ",", ' "cases": [']
lines.append(",\n".join('  {"name": %s, "text": %s,\n   "ids": %s}' % (json.dumps(n), json.dumps(t, ensure_ascii=False), json.dumps(ids(t)))
                        for n, t in cases))
lines += [" ]", "}"]
(out / "tokenizer_parity.json").write_text("\n".join(lines) + "\n")

write_vectors(out / "probe_golden.json", {"model": CATALOGUE_ID, "revision": CATALOGUE_REVISION}, PROBE)
write_vectors(out / "golden_vectors.json", {}, list(UDHR.values())[:10] + SYNTHETIC_SPEECH[:3])
print("prompt", repr(prompt), "cases", len(cases))
