#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Fast local browser QA without waiting for the full Controller foundation workflow.
#
# Usage:
#   scripts/run-browser-qa.sh                  # storage UI smoke (~10s warm)
#   scripts/run-browser-qa.sh smoke
#   scripts/run-browser-qa.sh late             # mocked/late browser steps
#   scripts/run-browser-qa.sh simulated        # gallery + managed VM + edit + ISO + storage
#   scripts/run-browser-qa.sh iso_url_browser.py
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

# Prefer project cache over /tmp — /tmp fills up on this host and breaks source builds.
export TMPDIR=${TMPDIR:-$ROOT/.cache/tmp}
mkdir -p "$TMPDIR" "$ROOT/.cache/browser-qa"

PY=${NEX_BROWSER_PY:-$ROOT/.cache/phase1-qa-venv/bin/python}
if [[ ! -x $PY ]]; then
  python3 -m venv "$ROOT/.cache/phase1-qa-venv"
  "$ROOT/.cache/phase1-qa-venv/bin/pip" install -q playwright==1.62.0 pillow==12.3.0
  "$ROOT/.cache/phase1-qa-venv/bin/python" -m playwright install chromium
  PY=$ROOT/.cache/phase1-qa-venv/bin/python
fi

if [[ ! -f web/dist/app.js ]]; then
  npm --prefix web ci
  npm --prefix web run build
fi

run() {
  local script=$1 evidence=$2
  shift 2
  echo "==> $script"
  local start=$SECONDS
  "$PY" "tests/integration/$script" --evidence "$evidence" "$@"
  echo "OK $script ($((SECONDS - start))s)"
}

target=${1:-smoke}
case "$target" in
  smoke)
    run iso_url_browser.py "$ROOT/.cache/browser-qa/iso-url"
    run storage_repositories_browser.py "$ROOT/.cache/browser-qa/storage"
    ;;
  late)
    run iso_url_browser.py "$ROOT/.cache/browser-qa/iso-url"
    run storage_repositories_browser.py "$ROOT/.cache/browser-qa/storage"
    run console_controls_browser.py "$ROOT/.cache/browser-qa/console-controls"
    run site_selector_browser.py "$ROOT/.cache/browser-qa/site-selector"
    run form_select_picker.py "$ROOT/.cache/browser-qa/form-select"
    ;;
  simulated)
    echo "==> website_controller_captures.py"
    start=$SECONDS
    "$PY" tests/integration/website_controller_captures.py \
      --output "$ROOT/.cache/browser-qa/gallery/images" \
      --evidence "$ROOT/.cache/browser-qa/gallery/results.json"
    echo "OK website_controller_captures.py ($((SECONDS - start))s)"
    run managed_vm_browser.py "$ROOT/.cache/browser-qa/managed-vm" --simulated
    run vm_edit_browser.py "$ROOT/.cache/browser-qa/vm-edit"
    run host_iso_browser.py "$ROOT/.cache/browser-qa/host-iso"
    run iso_url_browser.py "$ROOT/.cache/browser-qa/iso-url"
    run storage_repositories_browser.py "$ROOT/.cache/browser-qa/storage"
    ;;
  *.py)
    name=${target##*/}
    run "$name" "$ROOT/.cache/browser-qa/${name%.py}" "${@:2}"
    ;;
  *)
    echo "unknown target: $target" >&2
    echo "use: smoke | late | simulated | <script.py>" >&2
    exit 2
    ;;
esac
