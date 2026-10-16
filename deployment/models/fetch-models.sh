#!/bin/sh
# Download and unpack the sherpa-onnx model bundles an image ships with.
# The 110M model comes from its sherpa-onnx release archive. Parakeet Ultra
# (fp32 and int8) has no upstream archive: it comes file by file from the
# seekable Zstandard objects on dist.gocassini.com, pinned in
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
    parakeet-ultra-0.6b|parakeet-ultra-0.6b-int8)
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
    parakeet-ultra-0.6b)
      echo "encoder.onnx models/files/798e8acbe511295581a102819634a1d0025ba0e937d0ada29b7d2e5e9f0b7dba/encoder.onnx.zst 198042e45120cb9e03aac09bc5e47efb0024092866c0128973a13078a5f61ce2"
      echo "encoder.weights models/files/8509e45b901c712d2f627e1c4137d397833300e32474f96a0ce03eff938452f1/encoder.weights.zst 1209bc92c87a2f596767ae080aec04702d94a2a41eaba4cee5a89f69a9c22c12"
      echo "decoder.onnx models/files/e1bcde0da4b11cb2fc62ce70740ebb7afa14027f0873ad6565e5d25347ef9ff1/decoder.onnx.zst f0b644799b0a26286aecf82d4922ea344e8a6b782aedc846c2c8be29a2236403"
      echo "joiner.onnx models/files/6ba4dd9943a6088b33560b5ae65262f4fefe33bb83659b12b94849d1d86bbb2a/joiner.onnx.zst d15f8689b13d520a138ecfe440e6cb999cfd096c2a1a6fd27054c1dd61bcc302"
      echo "tokens.txt models/files/5b3811f0c23fefc86835981f0a23ecfb817999df272ad744c597e5b669ea1b4a/tokens.txt.zst d58544679ea4bc6ac563d1f545eb7d474bd6cfa467f0a6e2c1dc1c7d37e3c35d"
      echo "bpe.vocab models/files/4e074536b073a7d18ca034e3be779868696d67784765e4ac18511e8f607d4193/bpe.vocab.zst 41d5e71b3591642eff088151efd7acd4e750124cc0054c8ba9fa3245187a4804"
      ;;
    # Published without bpe.vocab, so it decodes greedily.
    parakeet-ultra-0.6b-int8)
      echo "encoder.int8.onnx models/files/caf7a38f2ad3224c1863564fb6e975a11aa3ae6ad1e7ad0bee5c4f5e018babb4/encoder.int8.onnx.zst 299617fefd4664d1c115b691dcb6413cabcdbcc694cdf3cb4464b72c5fc3ae69"
      echo "decoder.int8.onnx models/files/69e2e91d01225bc552f8b0113630562fc52622cb2a3c61ef4445ee779f06b295/decoder.int8.onnx.zst 1fab98fe6c12aded87d2da66272cc9e148d0a0044ce3850a12fe56302ec4a922"
      echo "joiner.int8.onnx models/files/639fda62a71242a8d7cefb6647f2094eb8ac783925ce91cbd047a0de6988f396/joiner.int8.onnx.zst 8a71aaccfdba3d451775507a889afd2257d2e754463fd81095a8c8f290f924c2"
      echo "tokens.txt models/files/5b3811f0c23fefc86835981f0a23ecfb817999df272ad744c597e5b669ea1b4a/tokens.txt.zst d58544679ea4bc6ac563d1f545eb7d474bd6cfa467f0a6e2c1dc1c7d37e3c35d"
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
    parakeet-ultra-0.6b-int8)      echo "encoder.int8.onnx decoder.int8.onnx joiner.int8.onnx tokens.txt" ;;
    # encoder.weights is external-data for encoder.onnx: sherpa cannot load the
    # encoder without it, so it is required, not a sidecar to copy if present.
    # A missing one must fail this build rather than bake a model that only
    # fails when a meeting is being transcribed.
    parakeet-ultra-0.6b) echo "encoder.onnx encoder.weights decoder.onnx joiner.onnx tokens.txt" ;;
    *) return 1 ;;
  esac
}

model_title() {
  case "$1" in
    parakeet-tdt-ctc-110m-en-int8) echo "NeMo Parakeet TDT CTC 110M en int8 (sherpa-onnx repack)" ;;
    parakeet-ultra-0.6b)           echo "Moondream Parakeet Ultra 0.6B fp32 (Cassini ONNX export)" ;;
    parakeet-ultra-0.6b-int8)      echo "Moondream Parakeet Ultra 0.6B int8 (Cassini ONNX export, dynamic int8 quantization)" ;;
    *) return 1 ;;
  esac
}

model_license() {
  case "$1" in
    parakeet-ultra-0.6b|parakeet-ultra-0.6b-int8)
      echo "License: cc-by-4.0 (Moondream Parakeet Ultra, https://huggingface.co/moondream/parakeet-ultra, post-trained from NVIDIA Parakeet, https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3)" ;;
    *)
      echo "License: cc-by-4.0 (NVIDIA Parakeet, https://huggingface.co/nvidia/parakeet-tdt_ctc-110m)" ;;
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
      "$(model_license "$id")" \
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
    "$(model_license "$id")" \
    "Bundled in: ghcr.io/codemyriad/gocassini" \
    > "$model_dir/NOTICE"

  rm -rf /tmp/model.tar.bz2 /tmp/extract
  ls -la "$model_dir/"
done
