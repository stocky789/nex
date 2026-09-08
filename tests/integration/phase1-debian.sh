#!/usr/bin/env bash
# SPDX-License-Identifier: AGPL-3.0-only
# Disposable Debian userspace + actual libvirt; no host devices or privileged mode.
set -euo pipefail
cd "$(dirname "$0")/../.."
repo=$PWD
work=$(mktemp -d /tmp/nex-phase1-debian.XXXXXX)
name="nex-phase1-debian-${work##*.}"
engine=(docker)
if ! docker info >/dev/null 2>&1; then engine=(sudo -n docker); fi
cleanup() { "${engine[@]}" rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"; }
trap cleanup EXIT
export GO=${GO:-$(command -v go || printf '%s' "$repo/.cache/toolchains/go/bin/go")}
"$GO" build -o "$work/controller" ./cmd/nex-controller
"$GO" build -o "$work/agent" ./cmd/nex-core-agent
python3 scripts/build-source.py --output "$work/source.tar.gz"
# Pin the registry manifest, including on clean CI workers.
image=debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132
if ! "${engine[@]}" image inspect "$image" >/dev/null 2>&1; then
  "${engine[@]}" pull "$image"
fi
"${engine[@]}" run --detach --name "$name" --label nex.test=phase1-debian \
  --mount "type=bind,src=$repo,dst=/repo,readonly" \
  --mount "type=bind,src=$work,dst=/artifacts,readonly" "$image" sleep infinity >/dev/null
"${engine[@]}" exec "$name" sh -c 'apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends libvirt-daemon-system libvirt-clients qemu-system-x86 python3 openssl >/tmp/packages.log 2>&1'
"${engine[@]}" exec -i "$name" python3 - <<'PY'
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time
spec = importlib.util.spec_from_file_location('phase1', '/repo/tests/integration/phase1.py')
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
run, eventually = module.run, module.eventually
print(run('cat', '/etc/os-release').stdout.strip())
print(run('dpkg-query', '-W', 'libvirt-daemon', 'libvirt-clients', 'qemu-system-x86').stdout.strip())
print(run('sha256sum', '/artifacts/controller', '/artifacts/agent').stdout.strip())
with tempfile.TemporaryDirectory(prefix='nex-native-') as directory:
    lab = module.Lab(Path(directory))
    try:
        cert, key = lab.path / 'tls.pem', lab.path / 'tls.key'
        run('openssl', 'req', '-x509', '-newkey', 'ec', '-pkeyopt', 'ec_paramgen_curve:P-256',
            '-nodes', '-keyout', key, '-out', cert, '-days', '1', '-subj', '/CN=localhost',
            '-addext', 'subjectAltName=DNS:localhost')
        password = lab.path / 'password'
        password.write_text('Disposable-Debian-8257!')
        password.chmod(0o600)
        cp = lab.start('controller', '/artifacts/controller', '-listen', f'127.0.0.1:{lab.port}',
            '-data-dir', lab.path / 'controller', '-tls-cert', cert, '-tls-key', key,
            '-admin-password-file', password, '-source-archive', '/artifacts/source.tar.gz',
            '-stale-after', '2s')
        lab.client(cert)
        eventually(lambda: lab.api('/api/v1/sites', expected=401), 'controller start')
        session = lab.api('/api/v1/session', {'username':'admin', 'password':password.read_text()})
        lab.csrf = session['csrf_token']
        site = lab.api('/api/v1/sites', {'name':'Debian smoke'}, expected=201)
        pool = lab.api('/api/v1/pools', {'name':'Disposable', 'site_id':site['id']}, expected=201)
        token = lab.path / 'token'
        token.write_text(lab.api('/api/v1/enrollment-tokens', {'pool_id':pool['id']}, expected=201)['token'])
        token.chmod(0o600)
        state = lab.path / 'agent'
        run('/artifacts/agent', 'enroll', '--state-dir', state, '--controller', lab.url,
            '--ca-file', cert, '--token-file', token)
        host_id = json.loads((state / 'state.json').read_text())['host_id']
        host = lambda: next(h for h in lab.api('/api/v1/hosts') if h['id']==host_id)
        def check(name, status):
            h = host()
            return h['connection']=='fresh' and h.get('inventory') and any(
                c['name']==name and c['status']==status for c in h['inventory']['checks'])
        run('libvirtd', '-d')
        eventually(lambda: subprocess.run(['virsh','--readonly','-c','qemu:///system','list','--all'],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode==0, 'libvirt start')
        ap = lab.start('agent', '/artifacts/agent', 'run', '--state-dir', state,
            '--heartbeat', '250ms', '--probe-interval', '250ms', '--probe-timeout', '1s')
        eventually(lambda: check('libvirt','healthy') and check('inventory','healthy'), 'native healthy probes')
        inv = host()['inventory']
        assert 'Debian GNU/Linux 13' in inv['os_version']
        assert inv['cpu_count']>0 and inv['memory_bytes']>0 and inv['nics'] and inv['disks']
        assert not inv.get('vms')
        assert check('kvm','unsupported')
        lab.passed('Debian 13 inventory and actual read-only qemu:///system healthy; absent KVM honestly unsupported')
        pid = int(Path('/run/libvirtd.pid').read_text())
        os.kill(pid, signal.SIGSTOP)
        eventually(lambda: check('libvirt','unknown'), 'real daemon stall deadline')
        seen = host()['last_seen']
        eventually(lambda: host()['last_seen']!=seen and check('inventory','healthy'), 'heartbeat during daemon stall')
        os.kill(pid, signal.SIGCONT)
        eventually(lambda: check('libvirt','healthy'), 'daemon resumes')
        lab.passed('SIGSTOP actual libvirtd: bounded unknown probe, responsive inventory/heartbeat, healthy after SIGCONT')
        os.kill(pid, signal.SIGTERM)
        eventually(lambda: check('libvirt','failed'), 'real daemon stopped')
        lab.passed('Stopped actual libvirtd: fresh host with failed libvirt service check')
        run('libvirtd', '-d')
        eventually(lambda: check('libvirt','healthy'), 'restarted daemon recovery')
        lab.passed('Restarted actual libvirtd: fresh healthy service observation restored')
        lab.stop(ap)
        eventually(lambda: host()['connection']=='stale', 'agent stop stale')
        lab.passed('Stopped agent: controller reports stale management connection')
    except Exception:
        for log in lab.path.glob('*.log'):
            print(log.name, log.read_text()[-4000:])
        raise
    finally:
        lab.close()
PY
