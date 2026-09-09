#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Read installed package evidence and observe exact lab VM boot via pinned SSH.

Requires console-installed public key and a private fresh-install lab config.
No product executable/service is replaced. QEMU guest-agent access is limited to
an explicitly supplied Controller UUID on this one freshly installed test host.
"""
import argparse
import base64
import hashlib
import json
from pathlib import Path
import shlex
import subprocess
import ssl
import tarfile
import xml.etree.ElementTree as ET
import time
from fresh_install_lab import ROOT, qmp, validate_lab


def ssh(lab, command, **kwargs):
    lab = validate_lab(lab)
    assert (lab / 'known_hosts').is_file() and not (lab / 'known_hosts').is_symlink(), 'existing pinned host identity required'
    config = json.loads((lab / 'config.json').read_text())
    return subprocess.run(['ssh', '-F', '/dev/null', '-o', 'StrictHostKeyChecking=yes', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5',
        '-o', f'UserKnownHostsFile={lab}/known_hosts', '-i', str(lab / 'id_ed25519'),
        '-p', str(config['ports']['ssh']), 'root@127.0.0.1', command], check=True,
        capture_output=True, **kwargs)


def guest(lab, vm_id, command):
    def call(body):
        return json.loads(ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', 'qemu-agent-command', vm_id, json.dumps(body)]), text=True).stdout)['return']
    pid = call({'execute': 'guest-exec', 'arguments': {'path': '/bin/sh', 'arg': ['-c', 'set -eu; ' + command], 'capture-output': True}})['pid']
    for _ in range(60):
        status = call({'execute': 'guest-exec-status', 'arguments': {'pid': pid}})
        if status.get('exited'):
            assert status.get('exitcode') == 0, 'guest evidence command failed'
            return base64.b64decode(status.get('out-data', '')).decode()
        time.sleep(.5)
    raise AssertionError('guest evidence command timed out')


def baseline(lab, evidence):
    assert ssh(lab, 'hostname', text=True).stdout.strip() == 'nex-fresh-lab'
    output = ssh(lab, '''set -eu
uname -a
cat /etc/os-release
dpkg-query -W nex-core-local qemu-system-x86 libvirt-daemon-system ovmf
systemctl is-active nex-core-local libvirtd ssh networking chrony
test -c /dev/kvm
test -d /sys/class/net/br0/bridge
ip -4 -o addr show br0
ip -4 route show
findmnt -no SOURCE,FSTYPE,OPTIONS /
df -B1 /var/lib/libvirt/images
systemctl cat nex-core-local
sha256sum /usr/lib/nex/nex-core-local /usr/share/nex/appliance/manifest.json /usr/share/nex/appliance/controller.qcow2 /usr/share/nex/core-local/nex-source.tar.gz
openssl x509 -in /var/lib/nex/core-local/tls.crt -noout -fingerprint -sha256
virsh -c qemu:///system list --all
cat /root/nex-qa-ssh-before /root/nex-qa-ssh-after
cat /etc/ssh/sshd_config.d/00-nex-core.conf /etc/ssh/sshd_config.d/00-00-nex-qa-key-only.conf
''', text=True).stdout
    (evidence / 'installed-host.txt').write_text(output)
    manifest = json.loads(ssh(lab, 'cat /usr/share/nex/appliance/manifest.json', text=True).stdout)
    assert manifest['version'] == '0.1.0+managed1'
    (evidence / 'installed-appliance-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    blocks = qmp(lab / 'qmp.sock', 'query-block')
    assert all(not b.get('inserted', {}).get('file', '').endswith('.iso') for b in blocks)
    config = json.loads((lab / 'config.json').read_text())
    config.setdefault('artifacts', {})['installed_manifest'] = manifest
    (lab / 'config.json').write_text(json.dumps(config, indent=2) + '\n')
    print('PASS: actual installed managed1 Core local package/embedded agent service, br0/KVM and signed bundle; boot has no ISO attached')


def provenance(lab, evidence, expected_controller):
    config = json.loads((lab / 'config.json').read_text())
    deployment = json.loads((evidence / 'browser-deployment-state.json').read_text())
    vm_id = deployment['operation']['vm_id']
    package = Path(config['iso']).parent / 'core-local/nex-core-local_0.1.0+managed1_amd64.deb'
    expected_local = {}
    proc = subprocess.Popen(['ar', 'p', str(package), 'data.tar.xz'], stdout=subprocess.PIPE)
    with tarfile.open(fileobj=proc.stdout, mode='r|xz') as archive:
        for member in archive:
            path = '/' + member.name.removeprefix('./')
            if member.isfile() and (path == '/usr/lib/nex/nex-core-local' or path.startswith('/usr/share/nex/core-local/web/') or path.startswith('/usr/share/nex/core-local/api/')):
                expected_local[path] = hashlib.file_digest(archive.extractfile(member), 'sha256').hexdigest()
    assert proc.wait() == 0
    expected_guest = json.loads(expected_controller.read_text())
    def command(paths):
        program = 'import hashlib,json,pathlib; print(json.dumps({p:hashlib.sha256(pathlib.Path(p).read_bytes()).hexdigest() for p in ' + repr(list(paths)) + '}))'
        return shlex.join(['python3', '-c', program])
    actual_local = json.loads(ssh(lab, command(expected_local), text=True).stdout)
    actual_guest = json.loads(guest(lab, vm_id, command(expected_guest)))
    assert actual_local == expected_local and actual_guest == expected_guest
    identity = json.loads(ssh(lab, 'cat /var/lib/nex-core-agent/state.json', text=True).stdout)
    controller_cert = guest(lab, vm_id, 'cat /etc/nex-controller/tls.crt')
    local_cert = ssh(lab, 'cat /var/lib/nex/core-local/tls.crt', text=True).stdout
    def digest(pem):
        return hashlib.sha256(ssl.PEM_cert_to_DER_cert(pem)).hexdigest()
    assert digest(identity['trust_pem']) == digest(controller_cert)
    assert digest(ssl.get_server_certificate(('127.0.0.1', config['ports']['controller']))) == digest(controller_cert)
    assert digest(ssl.get_server_certificate(('127.0.0.1', config['ports']['core']))) == digest(local_cert)
    xml = ET.fromstring(ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', 'dumpxml', vm_id]), text=True).stdout)
    assert all(not source.get('file') and not source.get('dev') for source in xml.findall("devices/disk[@device='cdrom']/source"))
    disk = Path(xml.find("devices/disk[@device='disk']/source").get('file'))
    ssh(lab, shlex.join(['test', '!', '-e', str(disk.parent / 'seed.iso')]), text=True)
    guest(lab, vm_id, 'test ! -e /etc/nex-controller/bootstrap.json; test ! -e /etc/nex-controller/admin-password; test ! -e /var/lib/cloud/instance/user-data.txt; test ! -e /run/cloud-init/instance-data-sensitive.json; test -e /var/lib/nex-controller/appliance-initialized')
    result = {'local_package_file_hashes_match': actual_local, 'controller_appliance_file_hashes_match': actual_guest,
        'controller_tls_sha256': digest(controller_cert), 'core_tls_sha256': digest(local_cert),
        'agent_trust_matches_served_controller_certificate': True, 'both_loopback_forward_tls_certificates_match_installed_certificates': True,
        'deployment_seed_removed_and_not_attached': True, 'bootstrap_password_and_sensitive_cloud_init_files_absent': True}
    (evidence / 'installed-provenance.json').write_text(json.dumps(result, indent=2) + '\n')
    print('PASS shipped binary/UI/API files match both actual installed services; TLS trust/forward certs match; deployment seed/bootstrap cleanup verified')


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--lab-dir', type=Path, required=True)
    p.add_argument('--evidence', type=Path, default=ROOT / 'docs/qa/fresh-install')
    p.add_argument('--expected-controller', type=Path)
    args = p.parse_args()
    if args.expected_controller:
        provenance(validate_lab(args.lab_dir), args.evidence, args.expected_controller)
    else:
        baseline(args.lab_dir, args.evidence)
