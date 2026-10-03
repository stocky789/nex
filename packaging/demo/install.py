#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Explicit root bootstrap; never opens firewall ports or grants Docker membership."""
import argparse
import grp
import ipaddress
import json
import os
from pathlib import Path
import pwd
import re
import shutil
import subprocess
import tempfile
from urllib.parse import urlsplit

ROOT = Path(__file__).resolve().parents[2]
UID = 64050


def install(path, data, mode=0o644, gid=0):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    if path.is_symlink():
        raise ValueError(f'refusing symlink destination: {path}')
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as file:
        temporary = Path(file.name)
        file.write(data.encode() if isinstance(data, str) else data)
        os.fchmod(file.fileno(), mode)
        os.fchown(file.fileno(), 0, gid)
    os.replace(temporary, path)


def directory(path, uid=0, gid=0, mode=0o700):
    path = Path(path)
    if path.is_symlink():
        raise ValueError(f'refusing symlink directory: {path}')
    path.mkdir(parents=True, exist_ok=True)
    os.chown(path, uid, gid)
    path.chmod(mode)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--listen', required=True, help='private IPv4 address:8081')
    parser.add_argument('--origin', required=True, help='fixed HTTPS public origin without port/path')
    parser.add_argument('--proxy-peer', action='append', required=True, help='allowed TCP peer IPv4 (repeat)')
    parser.add_argument('--trusted-proxy', action='append', default=[], help='exact peer trusted for XFF (repeat)')
    parser.add_argument('--deploy-user', required=True, help='existing CI publishing account')
    args = parser.parse_args()
    if os.geteuid() != 0:
        parser.error('run this bootstrap explicitly as root')
    address, port = args.listen.rsplit(':', 1)
    address = ipaddress.IPv4Address(address)
    private = ('127.0.0.0/8', '10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16', '100.64.0.0/10')
    if port != '8081' or not any(address in ipaddress.IPv4Network(net) for net in private):
        parser.error('--listen must bind a private or loopback IPv4 address on port 8081')
    peers = {str(ipaddress.IPv4Address(value)) for value in args.proxy_peer}
    trusted = {str(ipaddress.IPv4Address(value)) for value in args.trusted_proxy}
    if not trusted <= peers or '0.0.0.0' in peers:
        parser.error('trusted proxies must be exact allowed peer addresses, never a wildcard')
    origin = urlsplit(args.origin)
    hostname = origin.hostname or ''
    if (origin.scheme != 'https' or origin.netloc != hostname or origin.path or origin.query or origin.fragment
            or not re.fullmatch(r'[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+', hostname)):
        parser.error('--origin must be a lowercase HTTPS DNS origin without credentials, port or path')
    if not re.fullmatch(r'[a-z_][a-z0-9_-]*', args.deploy_user):
        parser.error('invalid publishing account name')
    publisher = pwd.getpwnam(args.deploy_user)
    if publisher.pw_uid == 0 or args.deploy_user == 'nex-demo':
        parser.error('publishing account must be nonroot and distinct from the sandbox account')
    for program in ('docker', 'nginx', 'curl', 'visudo', 'useradd', 'groupadd'):
        if shutil.which(program) is None:
            parser.error(f'install prerequisite: {program}')
    try:
        user = pwd.getpwnam('nex-demo')
    except KeyError:
        try:
            pwd.getpwuid(UID)
        except KeyError:
            pass
        else:
            parser.error('UID 64050 is already assigned to another account')
        try:
            group = grp.getgrgid(UID)
        except KeyError:
            subprocess.run(['/usr/sbin/groupadd', '--system', '--gid', str(UID), 'nex-demo'], check=True)
        else:
            if group.gr_name != 'nex-demo':
                parser.error('GID 64050 is already assigned to another group')
        subprocess.run(['/usr/sbin/useradd', '--system', '--uid', str(UID), '--gid', str(UID),
                        '--no-create-home', '--home-dir', '/nonexistent',
                        '--shell', '/usr/sbin/nologin', 'nex-demo'], check=True)
        user = pwd.getpwnam('nex-demo')
    if user.pw_uid != UID or user.pw_gid != UID:
        parser.error('nex-demo must have UID/GID 64050')
    try:
        docker_group = grp.getgrnam('docker')
    except KeyError:
        docker_group = None
    if docker_group and 'nex-demo' in docker_group.gr_mem:
        parser.error('remove nex-demo from the Docker group before installation')
    directory('/etc/nex-demo', gid=UID, mode=0o750)
    directory('/var/lib/nex-demo')
    directory('/run/nex-demo', uid=UID, gid=UID)
    directory('/run/nex-demo-proxy', uid=UID, gid=UID)
    packaging = ROOT / 'packaging/demo'
    config = (packaging / 'nginx.conf.template').read_text()
    config = config.replace('@NEX_DEMO_LISTEN@', args.listen).replace('@NEX_DEMO_HOST@', hostname)
    install('/etc/nex-demo/nginx.conf', config, mode=0o640, gid=UID)
    service = (packaging / 'nex-demo-proxy.service.template').read_text()
    service = service.replace('@NEX_DEMO_PROXY_PEER@', ' '.join(sorted(peers)))
    install('/etc/systemd/system/nex-demo-proxy.service', service)
    install('/etc/nex-demo/deploy.json', json.dumps({'NEX_DEMO_ORIGIN': args.origin}) + '\n', mode=0o600)
    install('/etc/nex-demo/trusted-proxies.conf',
            '# SPDX-License-Identifier: AGPL-3.0-only\n' +
            ''.join(f'set_real_ip_from {peer};\n' for peer in sorted(trusted)), mode=0o640, gid=UID)
    for source, destination, mode in (
        ('nex-demo-deploy', '/usr/local/sbin/nex-demo-deploy', 0o755),
        ('nex-demo-ssh-deploy', '/usr/local/sbin/nex-demo-ssh-deploy', 0o755),
        ('nex-demo.tmpfiles', '/etc/tmpfiles.d/nex-demo.conf', 0o644),
        ('nex-demo-journal.conf', '/etc/systemd/journald@nex-demo.conf', 0o644),
        ('nex-demo-docker.conf', '/etc/systemd/system/docker.service.d/nex-demo.conf', 0o644),
        ('nex-demo-socket.mount', '/etc/systemd/system/run-nex\\x2ddemo.mount', 0o644),
    ):
        install(destination, (packaging / source).read_bytes(), mode=mode)
    # Sudo 1.9.10+ supports anchored argument regex; no wildcard/extra arguments.
    rule = ('# SPDX-License-Identifier: AGPL-3.0-only\n' +
            f'{args.deploy_user} ALL=(root) NOPASSWD: /usr/local/sbin/nex-demo-deploy ^[0-9a-f]{{40}}$\n')
    with tempfile.NamedTemporaryFile(mode='w') as file:
        file.write(rule)
        file.flush()
        subprocess.run(['/usr/sbin/visudo', '-cf', file.name], check=True)
    install('/etc/sudoers.d/nex-demo-deploy', rule, mode=0o440)
    print('Installed root-owned files. No firewall or TLS edge changes were made.')
    print('Next: systemctl daemon-reload; systemd-tmpfiles --create /etc/tmpfiles.d/nex-demo.conf')
    print("Next: systemctl start 'run-nex\\x2ddemo.mount'")
    print('Next: runuser -u nex-demo -- nginx -t -c /etc/nex-demo/nginx.conf')
    print('Next: systemctl enable --now nex-demo-proxy.service; deploy first image through the wrapper')
    print('Existing broad sudo grants are not revoked; restrict the publishing account separately.')


if __name__ == '__main__':
    main()
