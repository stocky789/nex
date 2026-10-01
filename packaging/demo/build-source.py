#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Extend the Nex corresponding-source snapshot with the exact demo delivery inputs."""
import importlib.util
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
spec = importlib.util.spec_from_file_location('nex_source', ROOT / 'scripts/build-source.py')
source = importlib.util.module_from_spec(spec)
spec.loader.exec_module(source)
source.FILES += (
    'scripts/demo-ci-deploy.sh',
    '.gitea/workflows/demo.yml',
    '.github/workflows/demo.yml',
    'tests/integration/demo_browser.py',
    'tests/integration/demo_deploy_image.py',
    'tests/test_demo_archive.py',
)
source.FILES += tuple(str(path.relative_to(ROOT))
                      for path in sorted((ROOT / 'packaging/demo').rglob('*'))
                      if path.is_file() and '__pycache__' not in path.parts)

if __name__ == '__main__':
    source.main()
