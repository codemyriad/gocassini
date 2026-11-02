#!/usr/bin/env bash
# Offline checks of check-cuda-runtime.sh against small shared libraries built
# here with cc, so no CUDA, no image and no network. It covers the script's
# verdicts on the libraries (a clean set passes; an unresolved symbol, a
# missing library, or a file ldd cannot inspect fails). It does NOT cover the
# cuDNN lookup, which reads the host's ldconfig cache, or the real ORT CUDA
# provider against a real CUDA runtime: the CUDA base image build runs the
# script on those.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
names=(libonnxruntime_providers_cuda.so libonnxruntime.so libsherpa-onnx-c-api.so)

printf 'int cassini_ok(void) { return 1; }\n' >"$tmp/ok.c"
printf 'extern int cassini_absent(void);\nint cassini_bad(void) { return cassini_absent(); }\n' >"$tmp/bad.c"
# --no-as-needed keeps libc as a dependency, as every real library has one;
# ldd reports a library with none as "statically linked".
cc -shared -fPIC -Wl,--no-as-needed -o "$tmp/ok.so" "$tmp/ok.c" -lc
cc -shared -fPIC -Wl,--no-as-needed -o "$tmp/bad.so" "$tmp/bad.c" -lc

# lib_set DIR [NAME=SOURCE]... fills DIR with the three libraries (ok.so
# unless overridden) and a libcudnn.so.9 stand-in.
lib_set() {
  local dir=$1 name src
  shift
  mkdir -p "$dir"
  for name in "${names[@]}"; do cp "$tmp/ok.so" "$dir/$name"; done
  : >"$dir/libcudnn.so.9"
  for pair in "$@"; do
    name=${pair%%=*} src=${pair#*=}
    if [[ -z $src ]]; then rm -f "$dir/$name"; else cp "$src" "$dir/$name"; fi
  done
}

fails=0
expect() {
  local want=$1 label=$2 dir=$3
  if LD_LIBRARY_PATH='' "$root/check-cuda-runtime.sh" "$dir" >"$tmp/out" 2>&1; then got=pass; else got=fail; fi
  if [[ $got != "$want" ]]; then
    echo "check-cuda-runtime.sh: $label: got $got, want $want" >&2
    sed 's/^/  /' "$tmp/out" >&2
    fails=1
  fi
}

lib_set "$tmp/clean"
expect pass 'a clean library set' "$tmp/clean"

lib_set "$tmp/undefined" "libonnxruntime_providers_cuda.so=$tmp/bad.so"
expect fail 'an unresolved symbol' "$tmp/undefined"

printf 'not a library\n' >"$tmp/text.so"
lib_set "$tmp/text" "libonnxruntime_providers_cuda.so=$tmp/text.so"
expect fail 'a text file under a library name' "$tmp/text"

head -c 64 "$tmp/ok.so" >"$tmp/truncated.so"
lib_set "$tmp/truncated" "libonnxruntime.so=$tmp/truncated.so"
expect fail 'a truncated library' "$tmp/truncated"

lib_set "$tmp/absent" "libsherpa-onnx-c-api.so="
expect fail 'a missing library' "$tmp/absent"

[[ $fails == 0 ]] || exit 1
echo "check-cuda-runtime.sh tests passed"
