#!/usr/bin/env bash
set -euo pipefail

BENCH_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
BENCH_BIN=${PARAKEET_BENCH_BIN:-}
DIST_DIR=${SHERPA_DIST_DIR:-}
MODEL_DIR=${PARAKEET_MODEL_DIR:-}
VAD_MODEL=${SILERO_VAD_MODEL:-}
CUDA_LIB_DIRS=${CUDA_RUNTIME_LIB_DIRS:-/usr/local/cuda/lib64}
AUDIO_DIR="$BENCH_ROOT/fixtures/audio"
MANIFEST="$BENCH_ROOT/fixtures/manifest.v1.json"
HOTWORDS="$BENCH_ROOT/fixtures/hotwords.txt"
OUTPUT_DIR="$BENCH_ROOT/results/parakeet-hotword"
CONDITION_TIMEOUT_SECONDS=120
HOTWORD_SCORES=${HOTWORD_SCORES:-0.5,1.0,1.5}
MAX_ACTIVE_PATHS=${MAX_ACTIVE_PATHS:-4}

usage() {
  cat <<'EOF'
usage: parakeet_hotword.sh [options]

Required (flags or same-named environment variables):
  --bench-bin PATH       PARAKEET_BENCH_BIN: CUDA-linked hotword-bench binary
  --dist-dir PATH        SHERPA_DIST_DIR: libsherpa and ONNX Runtime CUDA .so files
  --model-dir PATH       PARAKEET_MODEL_DIR: Parakeet fp32 ONNX model directory
  --vad-model PATH       SILERO_VAD_MODEL: Silero VAD ONNX file

Options:
  --audio-dir PATH       verified fixture WAV directory
  --manifest PATH        immutable fixture manifest
  --hotwords PATH        one phrase per line
  --output-dir PATH      structured result directory
  --condition-timeout N  seconds per decoder condition (default 120)
  --scores CSV           conservative hotword scores (default 0.5,1.0,1.5)
  --max-active-paths N   modified beam paths, 1..8 (default 4)
  --cuda-lib-dirs LIST   colon-separated CUDA runtime directories
EOF
}

while (( $# )); do
  case "$1" in
    --bench-bin) BENCH_BIN=$2; shift 2 ;;
    --dist-dir) DIST_DIR=$2; shift 2 ;;
    --model-dir) MODEL_DIR=$2; shift 2 ;;
    --vad-model) VAD_MODEL=$2; shift 2 ;;
    --audio-dir) AUDIO_DIR=$2; shift 2 ;;
    --manifest) MANIFEST=$2; shift 2 ;;
    --hotwords) HOTWORDS=$2; shift 2 ;;
    --output-dir) OUTPUT_DIR=$2; shift 2 ;;
    --condition-timeout) CONDITION_TIMEOUT_SECONDS=$2; shift 2 ;;
    --scores) HOTWORD_SCORES=$2; shift 2 ;;
    --max-active-paths) MAX_ACTIVE_PATHS=$2; shift 2 ;;
    --cuda-lib-dirs) CUDA_LIB_DIRS=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ ! "$CONDITION_TIMEOUT_SECONDS" =~ ^[0-9]+$ ]] || \
    (( CONDITION_TIMEOUT_SECONDS < 10 || CONDITION_TIMEOUT_SECONDS > 900 )); then
  echo "--condition-timeout must be in 10..900" >&2
  exit 2
fi
if [[ ! "$MAX_ACTIVE_PATHS" =~ ^[0-9]+$ ]] || \
    (( MAX_ACTIVE_PATHS < 1 || MAX_ACTIVE_PATHS > 8 )); then
  echo "--max-active-paths must be in 1..8" >&2
  exit 2
fi

python3 "$BENCH_ROOT/scripts/verify_fixtures.py" --manifest "$MANIFEST" --audio-dir "$AUDIO_DIR"
for required in \
  "$BENCH_BIN" "$DIST_DIR/libsherpa-onnx-c-api.so" \
  "$DIST_DIR/libonnxruntime.so" "$DIST_DIR/libonnxruntime_providers_cuda.so" \
  "$MODEL_DIR/encoder.onnx" "$MODEL_DIR/encoder.weights" \
  "$MODEL_DIR/decoder.onnx" "$MODEL_DIR/joiner.onnx" \
  "$MODEL_DIR/tokens.txt" "$MODEL_DIR/bpe.vocab" "$VAD_MODEL" "$HOTWORDS"; do
  [[ -f "$required" ]] || { echo "missing required file: $required" >&2; exit 2; }
done
command -v nvidia-smi >/dev/null || { echo "nvidia-smi is required" >&2; exit 2; }
command -v jq >/dev/null || { echo "jq is required" >&2; exit 2; }

mapfile -t GPU_PROCESSES < <(nvidia-smi --query-compute-apps=pid,process_name,used_memory \
  --format=csv,noheader,nounits | sed '/^[[:space:]]*$/d')
if (( ${#GPU_PROCESSES[@]} )); then
  echo "refusing benchmark: NVIDIA compute workload already active" >&2
  printf '  %s\n' "${GPU_PROCESSES[@]}" >&2
  exit 2
fi

mkdir -p "$OUTPUT_DIR"
export CUDA_VISIBLE_DEVICES=${CUDA_VISIBLE_DEVICES:-0}
if [[ ! "$CUDA_VISIBLE_DEVICES" =~ ^[0-9]+$ ]]; then
  echo "CUDA_VISIBLE_DEVICES must select exactly one numeric GPU" >&2
  exit 2
fi
export GOMAXPROCS=1
export GOMEMLIMIT=${GOMEMLIMIT:-2GiB}
export OMP_NUM_THREADS=1
export OPENBLAS_NUM_THREADS=1
export MKL_NUM_THREADS=1
export LD_LIBRARY_PATH="$DIST_DIR:$CUDA_LIB_DIRS${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"

hash_file() {
  sha256sum "$1" | awk '{print $1}'
}
RUNTIME_INPUTS=$(jq -n \
  --arg bench "$(hash_file "$BENCH_BIN")" \
  --arg sherpa "$(hash_file "$DIST_DIR/libsherpa-onnx-c-api.so")" \
  --arg encoder "$(hash_file "$MODEL_DIR/encoder.onnx")" \
  --arg weights "$(hash_file "$MODEL_DIR/encoder.weights")" \
  --arg decoder "$(hash_file "$MODEL_DIR/decoder.onnx")" \
  --arg joiner "$(hash_file "$MODEL_DIR/joiner.onnx")" \
  --arg tokens "$(hash_file "$MODEL_DIR/tokens.txt")" \
  --arg vocab "$(hash_file "$MODEL_DIR/bpe.vocab")" \
  --arg vad "$(hash_file "$VAD_MODEL")" \
  --arg hotwords "$(hash_file "$HOTWORDS")" \
  '{hotwordBench:$bench,sherpa:$sherpa,encoder:$encoder,encoderWeights:$weights,
    decoder:$decoder,joiner:$joiner,tokens:$tokens,bpeVocab:$vocab,vad:$vad,hotwords:$hotwords}')

COMMON=(
  --model-dir "$MODEL_DIR"
  --vad-model "$VAD_MODEL"
  --audio "$AUDIO_DIR/2026-03-06_0742.wav"
  --audio "$AUDIO_DIR/2026-03-06_0961.wav"
)

validate_condition() {
  local label=$1
  jq -e '
    .schema == "gocassini.hotword-benchmark.v1" and
    .provider == "cuda" and .vadProvider == "cpu" and .numThreads == 1 and
    (.cudaMemoryMiBAtInit | type == "number" and . > 0)
  ' "$OUTPUT_DIR/$label.json.tmp" >/dev/null || {
    echo "$label did not independently prove CUDA decoding" >&2
    exit 2
  }
}

run_raw_condition() {
  local label=$1
  shift
  timeout --signal=TERM --kill-after=10s "$CONDITION_TIMEOUT_SECONDS" \
    "$BENCH_BIN" --label "$label" "${COMMON[@]}" "$@" \
    >"$OUTPUT_DIR/$label.json.tmp" 2>"$OUTPUT_DIR/$label.stderr"
  validate_condition "$label"
  mv "$OUTPUT_DIR/$label.json.tmp" "$OUTPUT_DIR/$label.json"
}

run_condition() {
  local label=$1
  shift
  run_raw_condition "$label" "$@"
  RESULT_FILES+=("$OUTPUT_DIR/$label.json")
}

guard_condition() {
  local label=$1
  "$BENCH_BIN" --mode guard \
    --baseline "$OUTPUT_DIR/greedy.json" \
    --candidate "$OUTPUT_DIR/$label.json" \
    --forbid-injected "Gocassini,HaRP,Nextcloud"
}

RESULT_FILES=()
# Seed CUDA/ONNX Runtime caches in an intentionally unscored first process.
# Its cold-start timing is retained separately instead of confounding the first
# decoder method in the comparison matrix.
run_raw_condition cold-start-greedy-unscored --method greedy_search
COLD_START_FILE="$OUTPUT_DIR/cold-start-greedy-unscored.json"
run_condition greedy --method greedy_search
run_condition mbs-no-hotwords --method modified_beam_search --max-active-paths "$MAX_ACTIVE_PATHS"
guard_condition mbs-no-hotwords

IFS=, read -r -a SCORES <<<"$HOTWORD_SCORES"
for score in "${SCORES[@]}"; do
  [[ "$score" =~ ^(0\.[1-9][0-9]*|1(\.[0-9]+)?|2(\.0+)?)$ ]] || {
    echo "hotword score must be >0 and <=2: $score" >&2; exit 2;
  }
  label="mbs-hotwords-${score//./_}"
  run_condition "$label" \
    --method modified_beam_search \
    --max-active-paths "$MAX_ACTIVE_PATHS" \
    --hotwords "$HOTWORDS" \
    --hotwords-score "$score"
  guard_condition "$label"
done

jq -s --slurpfile cold "$COLD_START_FILE" \
  --arg fixtureSet "$(jq -r '.fixtureSet' "$MANIFEST")" \
  --arg fixtureManifestSha256 "$(sha256sum "$MANIFEST" | awk '{print $1}')" \
  --argjson runtimeInputs "$RUNTIME_INPUTS" '{
  schema: "gocassini.parakeet-hotword-matrix.v1",
  fixtureSet: $fixtureSet,
  fixtureManifestSha256: $fixtureManifestSha256,
  runtimeInputs: $runtimeInputs,
  coldStart: $cold[0],
  conditions: [.[] | {
    label, provider, vadProvider, method, maxActivePaths, hotwordsScore,
    totalWords, initMs, vadMs, decodeMs, totalMs, audioSeconds,
    speechSeconds, decodeRtf, overallRtf, cudaMemoryMiBAtInit,
    audios: [.audios[] | {path, words, text, vadSegments, decodeMs}]
  }]
}' "${RESULT_FILES[@]}" >"$OUTPUT_DIR/summary.json.tmp"
mv "$OUTPUT_DIR/summary.json.tmp" "$OUTPUT_DIR/summary.json"
echo "$OUTPUT_DIR/summary.json"
