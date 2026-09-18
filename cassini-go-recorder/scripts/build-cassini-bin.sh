#!/usr/bin/env bash
# Build a Linux/macOS recorder with Cassini's model-reference native frontend.
# Usage: build-cassini-bin.sh [--backend cpu|cuda] [--test GO_TEST_ARGS... | --run GO_RUN_ARGS...]
# Requires Go, C++17 compiler, CMake >=3.15, curl, tar, patch, unzip, git.
# The native dependencies are isolated in .build-cache; go.mod/cache stay intact.
set -euo pipefail
rec=$(cd "$(dirname "$0")/.." && pwd)
repo=$(cd "$rec/.." && pwd)
backend=cpu; testing=0; running=0; output_bin=
while [[ $# -gt 0 ]]; do
  case "$1" in
    --backend) backend=$2; shift 2 ;;
    --output-bin) output_bin=$2; shift 2 ;;
    --test) testing=1; shift; break ;;
    --run) running=1; shift; break ;;
    --sherpa-version) [[ ${2#v} == 1.13.7 ]] || { echo 'Native frontend is pinned to sherpa 1.13.7' >&2; exit 2; }; shift 2 ;;
    -h|--help) sed -n '2,5p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done
case $(go env GOOS) in
 linux) binding=github.com/k2-fsa/sherpa-onnx-go-linux; extension=so
   case $(go env GOARCH) in
    amd64) triplet=x86_64-unknown-linux-gnu ;;
    arm64) triplet=aarch64-unknown-linux-gnu ;;
    arm) triplet=arm-unknown-linux-gnueabihf ;;
    *) echo 'Unsupported Linux architecture' >&2; exit 1 ;;
   esac ;;
 darwin) binding=github.com/k2-fsa/sherpa-onnx-go-macos; extension=dylib
   case $(go env GOARCH) in
    amd64) triplet=x86_64-apple-darwin ;;
    arm64) triplet=aarch64-apple-darwin ;;
    *) echo 'Unsupported macOS architecture' >&2; exit 1 ;;
   esac ;;
 *) echo 'Native packages support Linux/macOS.' >&2; exit 1 ;;
esac
[[ $(go env GOOS) == "$(go env GOHOSTOS)" && $(go env GOARCH) == "$(go env GOHOSTARCH)" ]] || { echo 'Build native packages on the target architecture (cross-compilation is unsupported).' >&2; exit 1; }

if [[ $backend == cpu ]]; then
  dist=$rec/dist
  mkdir -p "$dist"
  if [[ $running == 1 ]]; then
    exec go -C "$rec" run "$@"
  elif [[ $testing == 1 ]]; then
    if [[ $# == 0 ]]; then set -- ./internal/transcribe; fi
    exec go -C "$rec" test "$@"
  else
    output_bin=${output_bin:-$dist/cassini-bin}
    go -C "$rec" build -o "$output_bin" ./cmd/cassini
    echo "Built $output_bin using prebuilt native runtime."
    exit 0
  fi
fi

cache=$rec/.build-cache/native-$backend
dist=$rec/dist
mkdir -p "$cache" "$dist"
# Serialize preparation across CPU/CUDA invocations, including first-use CMake
# and Go binding extraction. Never hold this lock during recording or tests.
lock=$rec/.build-cache/native-prepare.lock
waited=0
until mkdir "$lock" 2>/dev/null; do
 owner=$(cat "$lock/pid" 2>/dev/null || true)
 if [[ $owner =~ ^[0-9]+$ ]] && ! kill -0 "$owner" 2>/dev/null; then
  echo "Stale native build lock $lock (PID $owner); remove it after checking no build is running." >&2
  exit 1
 fi
 if (( waited >= 600 )); then echo "Timed out waiting for native build lock $lock" >&2; exit 1; fi
 sleep 1
 waited=$((waited + 1))
done
printf '%s\n' "$$" > "$lock/pid"
release_lock() { rm -f "$lock/pid"; rmdir "$lock"; }
trap release_lock EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
fingerprint=$("$repo/deployment/sherpa/build.sh" --fingerprint)
if [[ ! -f $cache/lib/libsherpa-onnx-c-api.$extension ||
      $(cat "$cache/lib/cassini-native-inputs.sha256" 2>/dev/null || true) != "$fingerprint" ||
      -n ${SHERPA_ONNXRUNTIME_LIB_DIR:-} ]] ||
   ! grep -qx 'onnxruntime_identity=pinned-upstream-archive' "$cache/lib/cassini-native-buildinfo.txt" 2>/dev/null; then
  "$repo/deployment/sherpa/build.sh" "$backend" "$cache/lib" "$cache/work"
fi
shim=$cache/binding-ready-$fingerprint
if [[ ! -f $shim/go.mod ]]; then
 go -C "$rec" mod download "$binding"
 root=$(go -C "$rec" list -m -f '{{.Dir}}' "$binding")
 [[ -n $root && -f $root/go.mod ]] || { echo "Missing downloaded Go binding: $binding" >&2; exit 1; }
 staging_shim=$shim.tmp.$$
 mkdir -p "$staging_shim"
 cp -a "$root/." "$staging_shim/"
 chmod -R u+w "$staging_shim"
 mv "$staging_shim" "$shim"
fi
# Never truncate a library mapped by another running CLI/test process.
# Identical cached builds do not touch runtime files; changed files are renamed.
copy_runtime() {
 local source=$1 target=$2
 if [[ -f $target ]] && cmp -s "$source" "$target"; then return; fi
 cp "$source" "$target.tmp.$$"
 mv -f "$target.tmp.$$" "$target"
}
for native_file in "$cache/lib/"*."$extension"*; do
 copy_runtime "$native_file" "$shim/lib/$triplet/$(basename "$native_file")"
done
for native_file in "$cache/lib/"*; do
 copy_runtime "$native_file" "$dist/$(basename "$native_file")"
done
workspace=$cache/go-$fingerprint.work
cat > "$workspace.tmp.$$" <<WORK
go 1.24.0
use "$rec"
replace $binding => "$shim"
WORK
mv -f "$workspace.tmp.$$" "$workspace"
release_lock
trap - EXIT INT TERM
export GOWORK="$workspace"
export LD_LIBRARY_PATH="$shim/lib/$triplet${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}"
if [[ $extension == dylib ]]; then
  export DYLD_LIBRARY_PATH="$shim/lib/$triplet${DYLD_LIBRARY_PATH:+:$DYLD_LIBRARY_PATH}"
  export CGO_LDFLAGS="${CGO_LDFLAGS:-} -Wl,-rpath,@loader_path"
else
  export CGO_LDFLAGS="${CGO_LDFLAGS:-} -Wl,-rpath,\$ORIGIN"
fi
if [[ $running == 1 ]]; then
 go -C "$rec" run "$@"
elif [[ $testing == 1 ]]; then
 if [[ $# == 0 ]]; then set -- ./internal/transcribe; fi
 go -C "$rec" test "$@"
else
 output_bin=${output_bin:-$dist/cassini-bin}
 go -C "$rec" build -p 2 -o "$output_bin" ./cmd/cassini
 echo "Built $output_bin; native runtime libraries are in $dist/."
fi
