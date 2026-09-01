#!/bin/sh
# Download and unpack the sherpa-onnx model bundles an image ships with.
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
      echo "https://assets.gocassini.codemyriad.io/sherpa-onnx-nemo-parakeet-tdt-0.6b-v3.tar.bz2" ;;
    *) return 1 ;;
  esac
}

model_files() {
  case "$1" in
    parakeet-tdt-ctc-110m-en-int8) echo "model.int8.onnx tokens.txt" ;;
    parakeet-tdt-0.6b-v3-int8)     echo "encoder.int8.onnx decoder.int8.onnx joiner.int8.onnx tokens.txt" ;;
    parakeet-tdt-0.6b-v3)          echo "encoder.onnx decoder.onnx joiner.onnx tokens.txt" ;;
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
  mkdir -p "$model_dir" /tmp/extract
  echo "fetching $id from $url"
  curl -fSL "$url" -o /tmp/model.tar.bz2
  tar -xjf /tmp/model.tar.bz2 -C /tmp/extract
  # Archive top-level may be "sherpa-onnx-*" (upstream repacks) or the model id
  # itself (the fp32 release on assets.gocassini.codemyriad.io). Don't rely on
  # the name.
  src_dir=$(find /tmp/extract -mindepth 1 -maxdepth 1 -type d | head -n1)
  test -n "$src_dir" || { echo "no extracted dir under /tmp/extract:" >&2; ls /tmp/extract >&2; exit 1; }

  for f in $files; do
    test -f "$src_dir/$f" || { echo "missing $f in $id archive" >&2; ls "$src_dir" >&2; exit 1; }
    cp "$src_dir/$f" "$model_dir/"
  done
  # Optional sidecars: external onnx weights (the fp32 encoder references
  # encoder.weights via external_data and will not load without it) and the BPE
  # vocab some tokenizers ship.
  for f in encoder.weights bpe.vocab; do
    if [ -f "$src_dir/$f" ]; then
      cp "$src_dir/$f" "$model_dir/"
    fi
  done

  printf '%s\n' \
    "$title" \
    "Source: $url" \
    "License: cc-by-4.0 (NVIDIA Parakeet, https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3)" \
    "Bundled in: ghcr.io/codemyriad/gocassini" \
    > "$model_dir/NOTICE"

  rm -rf /tmp/model.tar.bz2 /tmp/extract
  ls -la "$model_dir/"
done
