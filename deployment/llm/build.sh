#!/usr/bin/env bash
# Build the same pinned runtime for portable CPU and CUDA images.
set -euo pipefail
backend=${1:?usage: build.sh cpu|cuda OUTPUT}
output=${2:?output directory required}
case "$backend" in cpu|cuda) ;; *) exit 2 ;; esac
revision=6a1a922d269908a29cbd4b49c27e6a8e7fd10fae
sha256=0bb4c8236625859b9db28792f62d3ff681023bc16ecff3dd55d602f30ae9ff47
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
curl -fLSs --retry 3 "https://codeload.github.com/ggml-org/llama.cpp/tar.gz/$revision" -o "$scratch/source.tar.gz"
printf '%s  %s\n' "$sha256" "$scratch/source.tar.gz" | sha256sum -c -
mkdir "$scratch/source"
tar -xzf "$scratch/source.tar.gz" -C "$scratch/source" --strip-components=1
options=(-DGGML_NATIVE=OFF -DGGML_CPU_ALL_VARIANTS=ON)
if [[ "$backend" == cuda ]]; then
  options+=(-DGGML_CUDA=ON '-DCMAKE_CUDA_ARCHITECTURES=75;80;86;89;90;100;120')
fi
cmake -S "$scratch/source" -B "$scratch/build" -G Ninja \
  -DCMAKE_BUILD_TYPE=Release -DLLAMA_BUILD_TESTS=OFF \
  -DLLAMA_BUILD_EXAMPLES=OFF -DGGML_BACKEND_DL=ON \
  -DCMAKE_BUILD_RPATH_USE_ORIGIN=ON "${options[@]}"
cmake --build "$scratch/build" -j "${CASSINI_LLM_BUILD_JOBS:-2}" --target llama-server
mkdir -p "$output"
cp -a "$scratch/build/bin/llama-server" "$scratch/build/bin/"*.so* "$output/"
cp "$scratch/source/LICENSE" "$output/LICENSE.llama.cpp"
printf 'llama.cpp %s\nbackend=%s\n' "$revision" "$backend" > "$output/BUILDINFO"
"$output/llama-server" --version
