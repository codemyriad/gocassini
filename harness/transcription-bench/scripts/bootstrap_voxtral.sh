#!/usr/bin/env bash
set -euo pipefail

BENCH_ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
VARIANT=${1:-}
case "$VARIANT" in
  offline|realtime) ;;
  *) echo "usage: $0 offline|realtime" >&2; exit 2 ;;
esac

PYTHON_BIN=${PYTHON_BIN:-python3}
VENV="$BENCH_ROOT/.venv-voxtral-$VARIANT"
"$PYTHON_BIN" - <<'PY'
import torch
if not torch.cuda.is_available():
    raise SystemExit("CUDA unavailable; refusing to prepare an inference worker")
print(torch.__version__, torch.version.cuda, torch.cuda.get_device_name(0))
PY

if [[ ! -x "$VENV/bin/python" ]]; then
  "$PYTHON_BIN" -m venv --system-site-packages "$VENV"
fi
"$VENV/bin/python" -m pip install --disable-pip-version-check --upgrade pip
"$VENV/bin/python" -m pip install --disable-pip-version-check \
  --requirement "$BENCH_ROOT/requirements/voxtral-$VARIANT.txt"
echo "$VENV/bin/python"
