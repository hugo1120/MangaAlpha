#!/usr/bin/env bash
set -euo pipefail

# Rebuild internal/httpserver/static/ui/codename-subset.woff2 from an LXGW WenKai TTF
# containing ONLY the characters required by buildinfo.Codename (e.g. "长夜").
# Usage:
#   bash scripts/build-codename-font.sh "长夜" /path/to/LXGWWenKai-Regular.ttf

TEXT="${1:-长夜}"
TTF_PATH="${2:-}"
OUT_DIR="internal/httpserver/static/ui"
OUT_FILE="${OUT_DIR}/codename-subset.woff2"

if [[ -z "${TTF_PATH}" || ! -f "${TTF_PATH}" ]]; then
  echo "Usage: $0 <codename-text> <path-to-LXGWWenKai-Regular.ttf>" >&2
  exit 1
fi

TMP_VENV="$(mktemp -d)"
trap 'rm -rf "${TMP_VENV}"' EXIT

python3 -m venv "${TMP_VENV}"
"${TMP_VENV}/bin/pip" install -q fonttools brotli
"${TMP_VENV}/bin/pyftsubset" "${TTF_PATH}" \
  --text="${TEXT}" \
  --flavor=woff2 \
  --layout-features="" \
  --no-hinting \
  --desubroutinize \
  --output-file="${OUT_FILE}"

ls -lh "${OUT_FILE}"
