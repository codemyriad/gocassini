# Distributing the topic suggestions model (maintainer step)

Date: 2026-10-14 · Parent: [README](README.md), slice S1

The catalogue entry `embeddinggemma-2-text-int8` (`cassini-go-recorder/internal/modelstore/catalogue.json`) points at three artifacts under `https://dist.gocassini.com/models/files/<sha>/`. Uploading them is a maintainer step. Until they are uploaded, `cassini models install` and the Settings download fail with a 404, and the model can only be installed with `cassini models import` from the upstream files.

## Sources

| File | Upstream | Bytes | sha256 |
|---|---|---|---|
| `model_quantized.onnx` | `onnx-community/embeddinggemma-2-ONNX@daa72c51243991dfcaf9f9137d2c573d8f7790c0`, `onnx/` | 495165 | `d06edd601f851c633a2519304cbeb8dc6170d7ceb61b436625c17fb9b6e74953` |
| `model_quantized.onnx_data` | same | 313724928 | `278a7ff1248c3618e4bd11a607fc54f7bdc7778854230f3956d3f86bd9db4f3b` |
| `tokenizer.model` | `google/embeddinggemma-2@914f7f89142e33e77833254d9c9b90c3cef7303b` | 4689013 | `e594c8a90eb08d8bda498ff4747977dc827ae0c3c56b5c0d41a605a22d02ef03` |

The data file must keep the name `model_quantized.onnx_data`: the graph refers to it by that name.

The model's `source_sha256` (its revision) is the sha256 of the `sha256sum` listing of the three files, sorted by name:

```sh
sha256sum model_quantized.onnx model_quantized.onnx_data tokenizer.model | sha256sum
# 89cef0454bc6e723f9f7fc53df0e2c881ba672fd00d8678c02d677f0b1ebee2c
```

## Artifacts

Each file is compressed on its own as seekable Zstandard, level 6, 262144-byte frames, like every other catalogue file. The catalogue records, for each artifact, the compressed `sha256` (also its id and key prefix), sizes, frame count and seek-table size. The entries in `catalogue.json` were computed on 2026-10-14. If the compressor gives different bytes, publish what it gives and update the three artifact entries (id, key, url, size, sha256, frame and seek-table fields); the uncompressed sizes and sha256 and the model revision do not change.

## Steps

1. Download the three files at the pinned commits and check the uncompressed sha256 above.
2. Compress each one and upload it to `models/files/<compressed sha256>/<name>.zst` on dist.gocassini.com.
3. Check the install from the CDN on a scratch cache root:
   ```sh
   cassini models install embeddinggemma-2-text-int8 --cache-root /tmp/cassini-models-check
   cassini models list --json --cache-root /tmp/cassini-models-check   # ready: true
   ```
   The install runs the probe: it embeds four pinned sentences and compares them with `internal/topics/probe_golden.json` (cosine ≥ 0.9995).

Offline hosts can import the upstream files directly:

```sh
cassini models import --from <dir with the three files> --model embeddinggemma-2-text-int8 \
  --revision 89cef0454bc6e723f9f7fc53df0e2c881ba672fd00d8678c02d677f0b1ebee2c
```
