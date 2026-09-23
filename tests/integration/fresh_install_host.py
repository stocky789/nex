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
import re
import secrets
import shlex
import subprocess
import ssl
import tarfile
import xml.etree.ElementTree as ET
import time
from fresh_install_lab import ROOT, qmp, validate_lab, validate_target


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


BIOS_FIXTURE_ROOT = '/var/lib/libvirt/images'
BIOS_FIXTURE_PREFIX = 'nex-fresh-bios-'


def fixture_source(path, expected_sha256):
    """Accept only an explicitly selected, regular private qcow fixture."""
    path = Path(path)
    assert re.fullmatch(r'[0-9a-f]{64}', expected_sha256), 'guest image SHA-256 is required'
    assert path.is_file() and not path.is_symlink(), 'guest image must be a regular non-symlink file'
    with path.open('rb') as image:
        actual = hashlib.file_digest(image, 'sha256').hexdigest()
    assert actual == expected_sha256, 'guest image digest differs'
    return path.resolve(strict=True)


def fixture_key(path):
    path = Path(path)
    assert path.is_file() and not path.is_symlink(), 'guest SSH key must be a regular non-symlink file'
    assert path.stat().st_mode & 0o077 == 0, 'guest SSH key must not be group/world accessible'
    return path.resolve(strict=True)


def fixture_host_key(path):
    path = Path(path)
    assert path.is_file() and not path.is_symlink(), 'guest SSH host key must be a regular non-symlink file'
    fields = path.read_text().strip().split()
    assert len(fields) in (2, 3) and fields[0].startswith('ssh-') and re.fullmatch(r'[A-Za-z0-9+/=]+', fields[1]), 'guest SSH host key must be one public OpenSSH key'
    return ' '.join(fields[:2])


def fixture_names():
    token = secrets.token_hex(12)
    return BIOS_FIXTURE_PREFIX + token, f'52:54:00:fa:{token[:2]}:{token[2:4]}'


def bios_domain_xml(name, image, mac):
    """A persistent BIOS-only guest definition with an independent qcow disk."""
    return f'''<domain type="kvm">
  <name>{name}</name><memory unit="MiB">512</memory><vcpu>1</vcpu>
  <os><type arch="x86_64">hvm</type><boot dev="hd"/></os><features><acpi/><apic/></features>
  <devices><emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type="file" device="disk"><driver name="qemu" type="qcow2"/><source file="{image}"/><target dev="vda" bus="virtio"/></disk>
    <interface type="network"><mac address="{mac}"/><source network="default"/><model type="virtio"/></interface>
  </devices>
</domain>'''


def independent_qcow_check():
    return '''import json, sys
source, disk = (json.load(open(path)) for path in sys.argv[1:])
assert source.get("format") == "qcow2"
assert disk.get("format") == "qcow2"
assert not disk.get("backing-filename") and not disk.get("full-backing-filename")
assert disk.get("virtual-size") == source.get("virtual-size") and disk.get("virtual-size", 0) > 0
'''


def scp_to_host(lab, source, destination):
    lab = validate_lab(lab)
    config = json.loads((lab / 'config.json').read_text())
    subprocess.run(['scp', '-F', '/dev/null', '-o', 'StrictHostKeyChecking=yes', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=10',
        '-o', f'UserKnownHostsFile={lab}/known_hosts', '-i', str(lab / 'id_ed25519'), '-P', str(config['ports']['ssh']),
        str(source), f'root@127.0.0.1:{destination}'], check=True, capture_output=True)


def bios_guest_ip(lab, domain):
    interfaces = ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', 'domiflist', domain]), text=True).stdout
    macs = [line.split()[4].lower() for line in interfaces.splitlines()
            if len(line.split()) >= 5 and re.fullmatch(r'(?:[0-9a-f]{2}:){5}[0-9a-f]{2}', line.split()[4].lower())]
    assert len(macs) == 1, 'fixture guest must have exactly one NIC MAC'
    leases = ssh(lab, 'virsh -c qemu:///system net-dhcp-leases default', text=True).stdout
    client_id = '01:' + macs[0]
    addresses = [fields[4].split('/', 1)[0] for fields in (line.split() for line in leases.splitlines())
                 if len(fields) >= 7 and fields[2].lower() == macs[0] and fields[3] == 'ipv4' and fields[6].lower() == client_id]
    assert len(addresses) == 1, 'fixture guest must have one exact DHCP lease'
    return addresses[0]


def bios_guest(lab, domain, key_path, known_hosts, script):
    address = bios_guest_ip(lab, domain)
    encoded = base64.b64encode(script.encode()).decode()
    command = (f'printf %s {shlex.quote(encoded)} | base64 -d | ssh -o BatchMode=yes '
               f'-o StrictHostKeyChecking=yes -o GlobalKnownHostsFile=/dev/null -o HostKeyAlias={shlex.quote(domain)} '
               f'-o UserKnownHostsFile={shlex.quote(known_hosts)} -o ConnectTimeout=5 -i {shlex.quote(key_path)} '
               f'root@{shlex.quote(address)} sh -s')
    return ssh(lab, command, text=True).stdout


def boot_id(output):
    value = output.strip().splitlines()[-1] if output.strip() else ''
    assert re.fullmatch(r'[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}', value), 'guest did not return an exact boot ID'
    return value


def changed_boot_id(output, previous_boot_id):
    value = boot_id(output)
    assert not previous_boot_id or value != previous_boot_id, 'guest boot ID did not change after reboot'
    return value


def wait_for_bios_guest(lab, domain, key_path, known_hosts, checksum='', previous_boot_id='', timeout=180):
    deadline, last = time.monotonic() + timeout, ''
    while time.monotonic() < deadline:
        try:
            changed = '' if not previous_boot_id else f'test "$(cat /proc/sys/kernel/random/boot_id)" != {shlex.quote(previous_boot_id)}; '
            check = 'cat /proc/sys/kernel/random/boot_id' if not checksum else f'sha256sum -c {shlex.quote(checksum)}; cat /proc/sys/kernel/random/boot_id'
            return changed_boot_id(bios_guest(lab, domain, key_path, known_hosts, 'set -eu; ' + changed + check), previous_boot_id)
        except (AssertionError, subprocess.CalledProcessError) as exc:
            last = str(exc)
            time.sleep(2)
    raise AssertionError(f'BIOS fixture guest did not become reachable: {last}')


def services_active_command(units):
    return 'for unit in ' + shlex.join(units) + '; do test "$(systemctl is-active "$unit")" = active || exit 1; done'


def wait_for_host_reboot(lab, previous_boot_id, timeout=300):
    deadline, last = time.monotonic() + timeout, ''
    while time.monotonic() < deadline:
        try:
            result = ssh(lab, 'set -eu; ' + services_active_command(('nex-core-local', 'libvirtd.socket', 'ssh')) + '; cat /proc/sys/kernel/random/boot_id', text=True).stdout
            current_boot_id = boot_id(result)
            if current_boot_id != previous_boot_id:
                return current_boot_id
        except subprocess.CalledProcessError as exc:
            last = str(exc)
        time.sleep(2)
    raise AssertionError(f'fresh host did not return after reboot: {last}')


def release_bios_fixture(lab, domain, fixture_dir):
    assert fixture_dir.startswith(BIOS_FIXTURE_ROOT + '/' + BIOS_FIXTURE_PREFIX)
    assert re.fullmatch(BIOS_FIXTURE_ROOT + r'/nex-fresh-bios-[0-9a-f]{24}', fixture_dir)
    command = (f'set -eu; test -d {shlex.quote(fixture_dir)}; test ! -L {shlex.quote(fixture_dir)}; '
               f'virsh -c qemu:///system shutdown {shlex.quote(domain)}; tries=0; '
               f'until test "$(virsh -c qemu:///system domstate {shlex.quote(domain)})" = "shut off"; do tries=$((tries + 1)); test "$tries" -lt 61; sleep 1; done; '
               f'virsh -c qemu:///system undefine {shlex.quote(domain)}; '
               f'if virsh -c qemu:///system dominfo {shlex.quote(domain)} >/dev/null 2>&1; then exit 1; fi; '
               f'test -f {shlex.quote(fixture_dir + "/guest.qcow2")}; test ! -L {shlex.quote(fixture_dir + "/guest.qcow2")}')
    return ssh(lab, command, text=True)


def bios_guest_persistence(lab, evidence, expected_version, image_path, key_path, host_key_path, image_sha256):
    """Run the disposable BIOS guest write, guest reboot and host reboot acceptance."""
    lab = validate_target(lab)
    evidence = Path(evidence)
    image, key, host_key = fixture_source(image_path, image_sha256), fixture_key(key_path), fixture_host_key(host_key_path)
    baseline(lab, evidence, expected_version)
    networks = ssh(lab, 'virsh -c qemu:///system net-list --name --autostart', text=True).stdout.splitlines()
    assert 'default' in networks, 'explicit fixture prerequisite: default NAT must already be active and autostart-enabled'
    name, mac = fixture_names()
    fixture_dir = BIOS_FIXTURE_ROOT + '/' + name
    remote_image, remote_key, remote_known_hosts = fixture_dir + '/base.qcow2', fixture_dir + '/guest_id_ed25519', fixture_dir + '/guest_known_hosts'
    disk, xml_path, checksum = fixture_dir + '/guest.qcow2', fixture_dir + '/guest.xml', '/root/nex-fresh-bios-persistence.sha256'
    record = {'completed': False, 'domain': name, 'fixture_dir': fixture_dir, 'base_sha256': image_sha256,
              'source': image.name, 'disk': disk, 'firmware': 'bios', 'network': 'libvirt default NAT'}
    (evidence / 'bios-guest-persistence.json').write_text(json.dumps(record, indent=2) + '\n')
    created, failure = False, None
    try:
        ssh(lab, 'set -eu; test -z "$(virsh -c qemu:///system list --name)"', text=True)
        ssh(lab, f'set -eu; mkdir -m 0711 {shlex.quote(fixture_dir)}; test -d {shlex.quote(fixture_dir)}; test ! -L {shlex.quote(fixture_dir)}', text=True)
        created = True
        scp_to_host(lab, image, remote_image)
        scp_to_host(lab, key, remote_key)
        encoded_check = base64.b64encode(independent_qcow_check().encode()).decode()
        ssh(lab, f'set -eu; chmod 600 {shlex.quote(remote_key)}; printf "%s %s\\n" {shlex.quote(name)} {shlex.quote(host_key)} > {shlex.quote(remote_known_hosts)}; chmod 600 {shlex.quote(remote_known_hosts)}; test ! -L {shlex.quote(remote_image)}; test ! -L {shlex.quote(remote_key)}; test ! -L {shlex.quote(remote_known_hosts)}; test "$(sha256sum {shlex.quote(remote_image)} | cut -d " " -f1)" = {shlex.quote(image_sha256)}; qemu-img info --output=json {shlex.quote(remote_image)} > {shlex.quote(fixture_dir + "/base.json")}; qemu-img convert -f qcow2 -O qcow2 {shlex.quote(remote_image)} {shlex.quote(disk)}; qemu-img info --output=json {shlex.quote(disk)} > {shlex.quote(fixture_dir + "/guest.json")}; printf %s {shlex.quote(encoded_check)} | base64 -d | python3 - {shlex.quote(fixture_dir + "/base.json")} {shlex.quote(fixture_dir + "/guest.json")}', text=True)
        xml = bios_domain_xml(name, disk, mac)
        ssh(lab, f'printf %s {shlex.quote(base64.b64encode(xml.encode()).decode())} | base64 -d > {shlex.quote(xml_path)}; virsh -c qemu:///system define {shlex.quote(xml_path)}; virsh -c qemu:///system start {shlex.quote(name)}', text=True)
        initial_boot = wait_for_bios_guest(lab, name, remote_key, remote_known_hosts)
        output = bios_guest(lab, name, remote_key, remote_known_hosts, f'set -eu; umask 077; dd if=/dev/urandom of=/root/nex-fresh-bios-persistence.bin bs=1M count=8 conv=fsync status=none; sha256sum /root/nex-fresh-bios-persistence.bin > {shlex.quote(checksum)}; sync; sha256sum -c {shlex.quote(checksum)}')
        assert 'OK' in output, 'guest did not verify the fsync data hash'
        ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', 'reboot', name]), text=True)
        guest_boot = wait_for_bios_guest(lab, name, remote_key, remote_known_hosts, checksum, initial_boot)
        ssh(lab, shlex.join(['virsh', '-c', 'qemu:///system', 'autostart', name]), text=True)
        host_boot = ssh(lab, 'cat /proc/sys/kernel/random/boot_id', text=True).stdout.strip()
        try:
            ssh(lab, 'systemctl reboot', text=True)
        except subprocess.CalledProcessError:
            pass  # A successful reboot commonly closes SSH before the client receives a status.
        new_host_boot = wait_for_host_reboot(lab, host_boot)
        final_guest_boot = wait_for_bios_guest(lab, name, remote_key, remote_known_hosts, checksum, guest_boot, timeout=300)
        record.update({'guest_boot_id_before': initial_boot, 'guest_boot_id_after_guest_reboot': guest_boot,
                       'guest_boot_id_after_host_reboot': final_guest_boot, 'host_boot_id_before': host_boot,
                       'host_boot_id_after': new_host_boot})
        release_bios_fixture(lab, name, fixture_dir)
        record['completed'] = True
        (evidence / 'bios-guest-persistence.json').write_text(json.dumps(record, indent=2) + '\n')
        print('PASS: independent BIOS guest disk hash survived guest and fresh-host reboots')
    except BaseException as exc:
        failure = exc
        record['error'] = str(exc)
        (evidence / 'bios-guest-persistence.json').write_text(json.dumps(record, indent=2) + '\n')
        raise
    finally:
        if created and failure is not None:
            record['retained_fixture'] = True
            (evidence / 'bios-guest-persistence.json').write_text(json.dumps(record, indent=2) + '\n')


def baseline(lab, evidence, expected_version='0.1.0+managed1'):
    assert ssh(lab, 'hostname', text=True).stdout.strip() == json.loads((lab / 'config.json').read_text())['hostname']
    ssh(lab, services_active_command(('nex-core-local', 'libvirtd.socket', 'ssh', 'networking', 'chrony')), text=True)
    output = ssh(lab, '''set -eu
uname -a
cat /etc/os-release
dpkg-query -W nex-core-local qemu-system-x86 libvirt-daemon-system ovmf-generic python3-virt-firmware
test "$(dpkg-query -W -f='${Version}' ovmf-generic)" = 2026.05-2
test -f /usr/share/qemu/firmware/40-edk2-x86_64-secure-enrolled.json
grep -Fq /usr/share/OVMF/OVMF_CODE_4M.ms.fd /usr/share/qemu/firmware/40-edk2-x86_64-secure-enrolled.json
grep -Fq /usr/share/OVMF/OVMF_VARS_4M.ms.fd /usr/share/qemu/firmware/40-edk2-x86_64-secure-enrolled.json
test -f /usr/share/OVMF/OVMF_CODE_4M.ms.fd
test -f /usr/share/OVMF/OVMF_VARS_4M.ms.fd
cert_dir=$(mktemp -d)
trap 'rm -rf "$cert_dir"' EXIT
(
    cd "$cert_dir"
    virt-fw-vars -i /usr/share/OVMF/OVMF_VARS_4M.ms.fd --extract-certs
)
check_ovmf_certificate() {
    expected_cn=$1
    expected_fingerprint=$2
    certificate=
    for candidate in "$cert_dir"/db-*.pem; do
        test -f "$candidate" || continue
        if openssl x509 -in "$candidate" -noout -subject | grep -Fq "CN=$expected_cn"; then
            test -z "$certificate"
            certificate=$candidate
        fi
    done
    test -n "$certificate"
    test "$(openssl x509 -in "$certificate" -noout -fingerprint -sha256 | cut -d= -f2)" = "$expected_fingerprint"
    openssl x509 -in "$certificate" -noout -checkend 0
}
check_ovmf_certificate 'Microsoft UEFI CA 2023' 'F6:12:4E:34:12:5B:EE:3F:E6:D7:9A:57:4E:AA:7B:91:C0:E7:BD:9D:92:9C:1A:32:11:78:EF:D6:11:DA:D9:01'
check_ovmf_certificate 'Windows UEFI CA 2023' '07:6F:1F:EA:90:AC:29:15:5E:BF:77:C1:76:82:F7:5F:1F:DD:1B:E1:96:DA:30:2D:C8:46:1E:35:0A:9A:E3:30'
rm -rf "$cert_dir"
trap - EXIT
test -f /usr/share/doc/nex-core/third-party/ovmf-generic/checksums.sha256
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
    assert manifest['version'] == expected_version
    (evidence / 'installed-appliance-manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    blocks = qmp(lab / 'qmp.sock', 'query-block')
    assert all(not b.get('inserted', {}).get('file', '').endswith('.iso') for b in blocks)
    config = json.loads((lab / 'config.json').read_text())
    config.setdefault('artifacts', {})['installed_manifest'] = manifest
    (lab / 'config.json').write_text(json.dumps(config, indent=2) + '\n')
    print('PASS: installed package/version, required services and br0/KVM; boot has no ISO attached (artifact provenance checked separately)')


def core_local_package(iso, version):
    """The nex-core-local package built next to the ISO under test, for the expected version."""
    return Path(iso).parent / 'core-local' / f'nex-core-local_{version}_amd64.deb'


def provenance(lab, evidence, expected_controller, expected_version):
    config = json.loads((lab / 'config.json').read_text())
    package = core_local_package(config['iso'], expected_version)
    assert package.is_file(), f'missing {package.name} next to the ISO under test; build it or pass the matching --expected-version'
    deployment = json.loads((evidence / 'browser-deployment-state.json').read_text())
    vm_id = deployment['operation']['vm_id']
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
    p.add_argument('--evidence', type=Path, required=True, help='new evidence directory; never the committed docs/qa evidence')
    p.add_argument('--expected-controller', type=Path)
    p.add_argument('--expected-version', default='0.1.0+managed1')
    p.add_argument('--bios-guest-persistence', action='store_true', help='exercise only a newly converted disposable BIOS guest disk')
    p.add_argument('--bios-guest-image', type=Path, help='verified private guest-base qcow2')
    p.add_argument('--bios-guest-key', type=Path, help='private SSH key accepted only by the disposable guest image')
    p.add_argument('--bios-guest-host-key', type=Path, help='verified public OpenSSH host key for the guest fixture')
    p.add_argument('--bios-guest-image-sha256', help='required SHA-256 of --bios-guest-image')
    args = p.parse_args()
    if args.bios_guest_persistence:
        assert not args.expected_controller, 'BIOS fixture acceptance does not use the Controller appliance guest'
        assert args.bios_guest_image and args.bios_guest_key and args.bios_guest_host_key and args.bios_guest_image_sha256, 'BIOS fixture image, client key, host key and digest are required'
        bios_guest_persistence(validate_lab(args.lab_dir), args.evidence, args.expected_version,
                               args.bios_guest_image, args.bios_guest_key, args.bios_guest_host_key, args.bios_guest_image_sha256)
    elif args.expected_controller:
        provenance(validate_lab(args.lab_dir), args.evidence, args.expected_controller, args.expected_version)
    else:
        baseline(validate_lab(args.lab_dir), args.evidence, args.expected_version)
