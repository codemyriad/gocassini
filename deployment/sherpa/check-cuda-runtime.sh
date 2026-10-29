#!/usr/bin/env bash
# Check that Cassini's CUDA libraries resolve against the CUDA runtime of the
# system they are installed on (run it inside the CUDA base image).
#
# ONNX Runtime's CUDA provider is loaded with dlopen only when a session asks
# for the CUDA execution provider, so a provider built for a newer CUDA 12.x
# runtime than the image's fails late: ORT 1.28.2's provider imports
# cudaLibraryLoadData, cudaLibraryGetKernel, cudaLibraryUnload and
# cudaKernelSetAttributeForDevice (libcudart.so.12, CUDA 12.5+), and on the
# CUDA 12.4 runtime it fails with "undefined symbol: cudaLibraryGetKernel,
# version libcudart.so.12". This check resolves every symbol now (ldd -r).
#
# Usage: check-cuda-runtime.sh LIB_DIR
# Extra library directories (for a check outside the image) come from
# LD_LIBRARY_PATH. libcuda.so.1 is the host driver's, mounted at run time; the
# image's forward-compat copy is used when present, and when it is absent only
# its unversioned driver symbols are excused.
set -euo pipefail
[[ $# -eq 1 ]] || { echo 'usage: check-cuda-runtime.sh LIB_DIR' >&2; exit 2; }
lib_dir=$(cd "$1" && pwd)
export LD_LIBRARY_PATH=$lib_dir${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}
if [[ -d /usr/local/cuda/compat ]]; then
  LD_LIBRARY_PATH=$LD_LIBRARY_PATH:/usr/local/cuda/compat
fi
status=0
for name in libonnxruntime_providers_cuda.so libonnxruntime.so libsherpa-onnx-c-api.so; do
  lib=$lib_dir/$name
  [[ -f $lib ]] || { echo "missing $lib" >&2; status=1; continue; }
  # ldd exits 0 with unresolved libraries or symbols; it fails only when it
  # cannot inspect the file at all ("not a dynamic executable": truncated,
  # corrupt, or not ELF), which the greps below would read as clean.
  if [[ $(head -c 4 "$lib" | od -An -tx1 | tr -d ' \n') != 7f454c46 ]]; then
    echo "$name is not an ELF file" >&2; status=1; continue
  fi
  if ! out=$(ldd -r "$lib" 2>&1); then
    echo "ldd cannot inspect $name:" >&2; echo "$out" >&2; status=1; continue
  fi
  # Every line must be a dependency (resolved or not found) or an undefined
  # symbol; anything else (a GLIBC version not found, a loader error) is a
  # failure the greps below do not look for.
  unexpected=$(grep -vE '^[[:space:]]+[^[:space:]]+( => ([^[:space:]]+ \(0x[0-9a-f]+\)|not found)| \(0x[0-9a-f]+\))$|^undefined symbol: ' <<<"$out" || true)
  if [[ -n $unexpected || -z $out ]]; then
    echo "$name does not load against this CUDA runtime:" >&2
    echo "${unexpected:-ldd printed nothing}" >&2
    status=1; continue
  fi
  missing=$(grep -F '=> not found' <<<"$out" | grep -vF 'libcuda.so.1' || true)
  # Provider_GetHost comes from libonnxruntime_providers_shared.so, which ORT
  # loads globally before the provider.
  undefined=$(grep '^undefined symbol:' <<<"$out" | grep -v '^undefined symbol: Provider_GetHost\b' || true)
  if grep -qF 'libcuda.so.1 => not found' <<<"$out"; then
    undefined=$(grep -F ', version ' <<<"$undefined" || true)
  fi
  if [[ -n $missing || -n $undefined ]]; then
    echo "$name does not load against this CUDA runtime:" >&2
    [[ -z $missing ]] || echo "$missing" >&2
    [[ -z $undefined ]] || echo "$undefined" >&2
    status=1
  fi
done
# The CUDA provider opens cuDNN 9 with dlopen, so ldd does not see it.
cudnn=no
if ldconfig -p 2>/dev/null | grep -qF 'libcudnn.so.9 '; then cudnn=yes; fi
IFS=: read -ra dirs <<<"$LD_LIBRARY_PATH"
for d in "${dirs[@]}"; do [[ -e $d/libcudnn.so.9 ]] && cudnn=yes; done
[[ $cudnn == yes ]] || { echo 'libcudnn.so.9 not found' >&2; status=1; }
if [[ $status == 0 ]]; then
  echo "CUDA libraries in $lib_dir resolve against this CUDA runtime"
fi
exit "$status"
