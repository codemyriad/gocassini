#!/bin/sh
# Download and unpack the sherpa-onnx model bundles an image ships with.
# Upstream int8 bundles come from the sherpa-onnx release archives. The fp32
# v3 model has no upstream archive: it comes file by file from the seekable
# Zstandard objects on dist.gocassini.com, pinned in
# cassini-go-recorder/internal/modelstore/catalogue.json.
#
#   fetch-models.sh <out-dir> <model-id>...
#
# Each model lands in <out-dir>/models/<model-id>/ with the exact file names the
# recorder looks for, next to a NOTICE recording its source and licence. The
# runtime images set CASSINI_DISALLOW_MODEL_DOWNLOAD=1, so whatever this script
# bundles is exactly the set of quality tiers that image can execute: a tier
# whose model is missing fails the build rather than dialling out.
#
# The catalog below mirrors knownModels in
# cassini-go-recorder/internal/transcribe/models.go — ids, URLs and file names
# must stay in step with it.

set -eu

usage() {
  echo "usage: $0 <out-dir> <model-id>..." >&2
  echo "       $0 --print-url <model-id>...   # resolve ids to URLs (CI hashing)" >&2
  exit 2
}

[ "$#" -ge 2 ] || usage

print_url_only=""
case "$1" in
  --print-url) print_url_only=1; shift ;;
  -*) usage ;;
  *) out_dir="$1"; shift ;;
esac

model_url() {
  case "$1" in
    parakeet-tdt-ctc-110m-en-int8)
      echo "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet_tdt_ctc_110m-en-36000-int8.tar.bz2" ;;
    parakeet-tdt-0.6b-v3-int8)
      echo "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8.tar.bz2" ;;
    parakeet-tdt-0.6b-v3)
      echo "https://dist.gocassini.com/manifest.json" ;;
    *) return 1 ;;
  esac
}

dist_base="https://dist.gocassini.com"

# "<file> <artifact key> <uncompressed sha256>" for the models served by
# dist.gocassini.com. Each key starts with the compressed sha256, so both
# digests are checked. Keep in step with catalogue.json.
dist_artifacts() {
  case "$1" in
    parakeet-tdt-0.6b-v3)
      echo "encoder.onnx models/files/2dff6d02d238425491fe8a2a9db8db45bdeb290b9e34643f6ae66df62bf9fd34/encoder.onnx.zst 7f30e635992b69c55df74fcd69d6af8dde0b0c07756a77c1b1f19c9354fb8283"
      echo "encoder.weights models/files/23a61cdbca1f606128fa9c2c05aa81457d3d0cce1aa7414a29f53ed5c626244d/encoder.weights.zst 3af3f51af5f2d01dbbf5af47d42c7962a2c205f11004254bb4f2b979862f39a8"
      echo "decoder.onnx models/files/e8038c3ce024db1f304cbdd59775ad1b063a6f2e8e49ea59e318721de4d606f4/decoder.onnx.zst cf13741631f0d3ae2f1bae07d9368af48aa314916eada9f96be68f09589f5eab"
      echo "joiner.onnx models/files/d74aa2809fe0186d99f961acfadceff72a7e42d9d56b51bd79f636d40609eeac/joiner.onnx.zst 24c96f15bc55f8a72039e0d4683983ae66d17d47432c26e147f4f8d945d63192"
      echo "tokens.txt models/files/5b3811f0c23fefc86835981f0a23ecfb817999df272ad744c597e5b669ea1b4a/tokens.txt.zst d58544679ea4bc6ac563d1f545eb7d474bd6cfa467f0a6e2c1dc1c7d37e3c35d"
      echo "bpe.vocab models/files/4e074536b073a7d18ca034e3be779868696d67784765e4ac18511e8f607d4193/bpe.vocab.zst 41d5e71b3591642eff088151efd7acd4e750124cc0054c8ba9fa3245187a4804"
      ;;
    *) return 1 ;;
  esac
}

sha256_of() {
  sha256sum "$1" | cut -d' ' -f1
}

# fetch_dist <model-id> <model-dir>: download, verify and decompress each file.
fetch_dist() {
  dist_artifacts "$1" | while read -r name key digest; do
    compressed_digest=$(echo "$key" | cut -d/ -f3)
    echo "fetching $name from $dist_base/$key"
    curl -fSL --retry 3 "$dist_base/$key" -o /tmp/model-file.zst
    test "$(sha256_of /tmp/model-file.zst)" = "$compressed_digest" ||
      { echo "sha256 mismatch for $key" >&2; exit 1; }
    zstd -dqf /tmp/model-file.zst -o "$2/$name"
    rm -f /tmp/model-file.zst
    test "$(sha256_of "$2/$name")" = "$digest" ||
      { echo "sha256 mismatch for decompressed $name" >&2; exit 1; }
  done
}

model_files() {
  case "$1" in
    parakeet-tdt-ctc-110m-en-int8) echo "model.int8.onnx tokens.txt" ;;
    parakeet-tdt-0.6b-v3-int8)     echo "encoder.int8.onnx decoder.int8.onnx joiner.int8.onnx tokens.txt" ;;
    # encoder.weights is external-data for encoder.onnx: sherpa cannot load the
    # encoder without it, so it is required, not a sidecar to copy if present.
    # A missing one must fail this build rather than bake a model that only
    # fails when a meeting is being transcribed.
    parakeet-tdt-0.6b-v3)          echo "encoder.onnx encoder.weights decoder.onnx joiner.onnx tokens.txt" ;;
    *) return 1 ;;
  esac
}

model_title() {
  case "$1" in
    parakeet-tdt-ctc-110m-en-int8) echo "NeMo Parakeet TDT CTC 110M en int8 (sherpa-onnx repack)" ;;
    parakeet-tdt-0.6b-v3-int8)     echo "NeMo Parakeet TDT 0.6B v3 int8 (sherpa-onnx repack)" ;;
    parakeet-tdt-0.6b-v3)          echo "NeMo Parakeet TDT 0.6B v3 fp32 (sherpa-onnx repack)" ;;
    *) return 1 ;;
  esac
}

for id in "$@"; do
  url=$(model_url "$id") || { echo "unknown model id: $id" >&2; exit 1; }
  if [ -n "$print_url_only" ]; then
    echo "$url"
    continue
  fi
  files=$(model_files "$id")
  title=$(model_title "$id")

  model_dir="$out_dir/models/$id"
  mkdir -p "$model_dir"

  if dist_artifacts "$id" >/dev/null; then
    fetch_dist "$id" "$model_dir"
    for f in $files; do
      test -f "$model_dir/$f" || { echo "missing $f for $id" >&2; exit 1; }
    done
    printf '%s\n' \
      "$title" \
      "Source: $url (seekable Zstandard files, decompressed)" \
      "License: cc-by-4.0 (NVIDIA Parakeet, https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3)" \
      "Bundled in: ghcr.io/codemyriad/gocassini" \
      > "$model_dir/NOTICE"
    ls -la "$model_dir/"
    continue
  fi

  mkdir -p /tmp/extract
  echo "fetching $id from $url"
  curl -fSL "$url" -o /tmp/model.tar.bz2
  tar -xjf /tmp/model.tar.bz2 -C /tmp/extract
  # Archive top-level is normally "sherpa-onnx-*" (upstream repacks). Don't
  # rely on the name.
  src_dir=$(find /tmp/extract -mindepth 1 -maxdepth 1 -type d | head -n1)
  test -n "$src_dir" || { echo "no extracted dir under /tmp/extract:" >&2; ls /tmp/extract >&2; exit 1; }

  for f in $files; do
    test -f "$src_dir/$f" || { echo "missing $f in $id archive" >&2; ls "$src_dir" >&2; exit 1; }
    cp "$src_dir/$f" "$model_dir/"
  done
  # Optional extra: the BPE vocab some tokenizers ship. Anything a model cannot
  # load without belongs in model_files above instead.
  if [ -f "$src_dir/bpe.vocab" ]; then
    cp "$src_dir/bpe.vocab" "$model_dir/"
  fi

  printf '%s\n' \
    "$title" \
    "Source: $url" \
    "License: cc-by-4.0 (NVIDIA Parakeet, https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3)" \
    "Bundled in: ghcr.io/codemyriad/gocassini" \
    > "$model_dir/NOTICE"

  rm -rf /tmp/model.tar.bz2 /tmp/extract
  ls -la "$model_dir/"
done
