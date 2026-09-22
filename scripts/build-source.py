#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Snapshot corresponding source; never include runtime state or private keys."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
FILES = ('go.mod', 'go.sum', 'LICENSE', 'NOTICE', 'ARTWORK-LICENSE.md', 'Makefile',
         'cmd/nex-controller/main.go', 'cmd/nex-core-agent/main.go',
         'web/public/nex-brand.png', 'web/public/third-party-notices.txt', 'docs/brand/README.md',
         'web/package.json', 'web/package-lock.json', 'web/build.mjs', 'web/tsconfig.json',
         'docs/development.md', 'docs/controller-dependencies.md', 'docs/LICENSING.md',
         'appliance/Dockerfile', 'packaging/core-local/Dockerfile',
         'packaging/cluster-storage/Dockerfile',
         'packaging/cluster-storage/cluster-iscsi-session',
         'packaging/core-local/core-local-setup', 'packaging/core-local/corosync-pristine', 'packaging/core-local/qemu-vnc-lossy',
         'packaging/core-local/preinst',
         'packaging/core-local/postinst', 'packaging/core-local/prerm',
         'packaging/controller/Dockerfile', 'packaging/controller/run-controller',
         'packaging/controller/apply-update', 'packaging/controller/postinst',
         'packaging/controller/prerm',
         'docs/API.md', 'docs/api/openapi.json', 'docs/api/index.html', 'docs/api/reference.css')
TREES = {'cmd': {'.go'}, 'internal': {'.go', '.sql'}, 'web/src': {'.tsx', '.ts', '.css'},
         'web/public': {'.html'}, 'packaging/systemd': {'.service'},
         'appliance': {'.py', '.sh', '.service', '.md'},
         'packaging/core-local': {'.py', '.sh', '.service', '.md', '.c', '.pam', '.example', '.gpg',
                                  '.conf'},
         'packaging/controller': {'.py', '.sh', '.service', '.path', '.md'},
         'packaging/cluster-storage': {'.sh'},
         'docs/api': {'.json', '.html', '.css'}}
SCRIPTS = ('scripts/build-source.py', 'tests/test_build_source.py',
           'scripts/build-source-bundle.py', 'scripts/prune-iso-packages.py',
           'scripts/release-publish.py', 'tests/test_source_bundle.py',
           'tests/test_prune_iso_packages.py', 'tests/test_release_publish.py',
           'scripts/generate_api.py', 'tests/test_api_contract.py',
           'tests/test_agent_service.py', 'tests/integration/phase1.py',
           'tests/integration/managed_vm.py', 'tests/integration/managed_vm_lab.py',
           'tests/integration/managed_vm_browser.py',
           'tests/integration/console_lab.py',
           'tests/test_appliance_build.py', 'tests/test_core_local_package.py',
           'tests/test_host_recovery.py', 'tests/integration/localcore_browser.py',
           'tests/integration/web_security.py',
           'tests/integration/appliance-lab.py', 'tests/integration/appliance-dhcp-boot.py',
           'tests/integration/core-local-package.sh',
           'tests/integration/cluster_storage_workloads.py',
           'tests/integration/cluster_ha_lab.py',
           'tests/test_cluster_storage_workloads.py', 'tests/test_cluster_ha_lab.py',
           'scripts/apt-publish.py', 'tests/test_apt_publish.py')


def copy_sources(root, target):
    paths = [root / name for name in FILES + SCRIPTS]
    for name, suffixes in TREES.items():
        paths.extend(p for p in (root / name).rglob('*') if p.is_file() and p.suffix in suffixes)
    for source in sorted(set(paths)):
        if source.is_symlink() or any(p.is_symlink() for p in source.parents if p != root.parent):
            raise ValueError(f'symlink refused: {source}')
        destination = target / source.relative_to(root)
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, destination)


GO_LICENSE_SYSTEM_PATHS = (Path('/usr/share/licenses/go/LICENSE'),)


def go_license_path(goroot, system_candidates=GO_LICENSE_SYSTEM_PATHS):
    """Locate the Go toolchain's LICENSE file.

    Official go.dev/actions-setup-go distributions ship it at GOROOT/LICENSE.
    Distro-packaged Go (e.g. Arch/CachyOS `go`) omits it from GOROOT and
    installs it under the system license directory instead.
    """
    override = os.environ.get('GO_LICENSE')
    if override:
        return Path(override)
    for candidate in (goroot / 'LICENSE', *system_candidates):
        if candidate.is_file():
            return candidate
    searched = ', '.join(str(p) for p in (goroot / 'LICENSE', *system_candidates))
    raise RuntimeError(f'no Go LICENSE file found (searched {searched}); set GO_LICENSE to its path')


def verify(data, integrity):
    algorithm, expected = integrity.split('-', 1)
    if algorithm not in ('sha256', 'sha384', 'sha512'):
        raise ValueError('unsupported npm integrity algorithm')
    if base64.b64encode(hashlib.new(algorithm, data).digest()).decode() != expected:
        raise ValueError('npm package integrity mismatch')


def write_archive(target, output):
    def public_metadata(info):
        info.uid = info.gid = 0
        info.uname = info.gname = ''
        info.pax_headers = {}
        return info
    with tarfile.open(output, 'w:gz') as archive:
        archive.add(target, arcname='nex-source', filter=public_metadata)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', default='dist/nex-source.tar.gz')
    args = parser.parse_args()
    output = Path(args.output).resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='nex-source-') as temporary:
        target = Path(temporary) / 'nex-source'
        target.mkdir()
        copy_sources(ROOT, target)
        go = os.environ.get('GO', 'go')
        subprocess.run([go, 'mod', 'vendor'], cwd=target, check=True)
        goroot = Path(subprocess.check_output([go, 'env', 'GOROOT'], text=True).strip())
        toolchain = target / 'third_party/go'
        toolchain.mkdir(parents=True)
        shutil.copyfile(go_license_path(goroot), toolchain / 'LICENSE')
        (toolchain / 'VERSION').write_text(subprocess.check_output([go, 'version'], text=True))
        packages = json.loads((target / 'web/package-lock.json').read_text())['packages']
        cache = ROOT / '.cache/source-packages'
        cache.mkdir(parents=True, exist_ok=True)
        npm = target / 'third_party/npm'
        npm.mkdir(parents=True)
        for name, package in sorted(packages.items()):
            if not name:
                continue
            url = package['resolved']
            if not url.startswith('https://registry.npmjs.org/'):
                raise ValueError(f'unreviewed dependency origin: {url}')
            filename = name.removeprefix('node_modules/').replace('/', '-') + '-' + package['version'] + '.tgz'
            cached = cache / filename
            if not cached.exists():
                with urllib.request.urlopen(url, timeout=60) as response:
                    data = response.read()
                verify(data, package['integrity'])
                cached.write_bytes(data)
            verify(cached.read_bytes(), package['integrity'])
            shutil.copyfile(cached, npm / filename)
        manifest = {str(p.relative_to(target)): hashlib.sha256(p.read_bytes()).hexdigest()
                    for p in sorted(target.rglob('*')) if p.is_file()}
        (target / 'SOURCE-MANIFEST.json').write_text(json.dumps(manifest, indent=2) + '\n')
        write_archive(target, output)
    digest = hashlib.sha256(output.read_bytes()).hexdigest()
    output.with_suffix(output.suffix + '.sha256').write_text(f'{digest}  {output.name}\n')
    print(f'{output}: {digest}')


if __name__ == '__main__':
    main()
