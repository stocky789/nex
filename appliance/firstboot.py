#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Consume host-provided NoCloud credentials exactly once, without logging them."""
import json
import os
from pathlib import Path
import pwd
import shutil
import ssl
import subprocess
import time
import urllib.request

CONFIG = Path('/etc/nex-controller')
STATE = Path('/var/lib/nex-controller')


def validate(config):
    for field in ('admin_password', 'tls_certificate', 'tls_private_key'):
        if not isinstance(config.get(field), str) or not config[field]:
            raise ValueError('missing bootstrap credential')
    password = config['admin_password']
    if len(password.encode('utf-8')) > 1024 or any(char in password for char in '\x00\r\n'):
        raise ValueError('invalid administrator password')
    if '-----BEGIN CERTIFICATE-----' not in config['tls_certificate']:
        raise ValueError('invalid TLS certificate')
    if 'PRIVATE KEY-----' not in config['tls_private_key']:
        raise ValueError('invalid TLS private key')


def main():
    os.umask(0o077)
    bootstrap = CONFIG / 'bootstrap.json'
    if bootstrap.exists():
        config = json.loads(bootstrap.read_text())
        validate(config)
        user = pwd.getpwnam('nex-controller')
        STATE.mkdir(mode=0o700, exist_ok=True)
        os.chown(STATE, user.pw_uid, user.pw_gid)
        os.chown(CONFIG, user.pw_uid, user.pw_gid)
        for field, filename in [('admin_password', 'admin-password'), ('tls_certificate', 'tls.crt'), ('tls_private_key', 'tls.key')]:
            target = CONFIG / filename
            target.write_text(config[field])
            target.chmod(0o600)
            os.chown(target, user.pw_uid, user.pw_gid)
    elif not all(path.is_file() for path in (CONFIG / 'tls.crt', CONFIG / 'tls.key', STATE / 'controller.db')):
        raise RuntimeError('bootstrap is missing before controller initialization')
    subprocess.run(['systemctl', 'start', 'nex-controller'], check=True)
    # Trust only this deployment's certificate; loopback has a different hostname.
    context = ssl.create_default_context(cafile=str(CONFIG / 'tls.crt'))
    context.check_hostname = False
    for _ in range(90):
        try:
            with urllib.request.urlopen('https://127.0.0.1:8443/', context=context, timeout=2) as response:
                if response.status == 200:
                    break
        except (OSError, ValueError):
            time.sleep(1)
    else:
        raise RuntimeError('controller did not become ready; bootstrap retained for diagnosis')
    # Preserve generated networking, prevent removed NoCloud data rerunning on reboot.
    Path('/etc/cloud/cloud-init.disabled').touch(mode=0o600)
    for filename in ('bootstrap.json', 'admin-password'):
        (CONFIG / filename).unlink(missing_ok=True)
    for cache in ('/var/lib/cloud', '/run/cloud-init'):
        if Path(cache).exists():
            shutil.rmtree(cache)
    for filename in ('/var/log/cloud-init.log', '/var/log/cloud-init-output.log'):
        Path(filename).unlink(missing_ok=True)
    ready = Path('/usr/local/share/nex/dist/appliance-ready.json')
    temporary = ready.with_suffix('.tmp')
    temporary.write_text(json.dumps({'bootstrap_version': 1, 'status': 'ready'}) + '\n')
    temporary.chmod(0o644)
    temporary.replace(ready)
    (STATE / 'appliance-initialized').write_text('1\n')
    # Host detaches and deletes the original seed after readiness; guest cannot erase CDROM.


if __name__ == '__main__':
    main()
