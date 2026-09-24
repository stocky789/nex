#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Exercise the shipped Debian account dialog with Nex's hook; no host accounts change.

python3 tests/integration/root-account.py PATH/TO/user-setup-udeb_VERSION_all.udeb
Requires Python, ar and the shell tools used by the installer hook.
"""
import io
import os
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile


MOCK_DEBCONF = r'''
root_login=true make_user=false root_pw='' root_again='' root_hash='' attempts=0
db_capb() { :; }
db_fset() { :; }
db_get() {
    case "$1" in
        passwd/root-login) RET=$root_login;;
        passwd/make-user) RET=$make_user;;
        passwd/root-password) RET=$root_pw;;
        passwd/root-password-again) RET=$root_again;;
        passwd/root-password-crypted) RET=$root_hash;;
        *) RET='';;
    esac
}
db_set() {
    case "$1" in
        passwd/root-login) root_login=$2;;
        passwd/make-user) make_user=$2;;
        passwd/root-password) root_pw=$2;;
        passwd/root-password-again) root_again=$2;;
        passwd/root-password-crypted) root_hash=$2;;
    esac
}
db_input() {
    case "$2" in
        passwd/root-login|passwd/make-user) return 30;;
        passwd/user-*|passwd/username*) echo "REGULAR USER PROMPT: $2"; exit 91;;
    esac
    echo "PROMPT $2"
}
db_go() {
    if [ "$STATE" = 1 ] && [ "$root_login" = true ]; then
        attempts=$((attempts + 1))
        root_pw='test-only-root-password' root_again='test-only-root-password'
        if [ "$attempts" = 1 ]; then
            case "$SCENARIO" in
                blank) root_pw='' root_again='';;
                mismatch) root_again='different';;
            esac
        fi
    fi
}
'''


def main():
    repo = Path(__file__).resolve().parents[2]
    data = subprocess.check_output(['ar', 'p', sys.argv[1], 'data.tar.xz'])
    with tarfile.open(fileobj=io.BytesIO(data)) as archive:
        native = archive.extractfile('./usr/bin/user-setup-ask').read().decode()
        helpers = archive.extractfile('./usr/lib/user-setup/functions.sh').read().decode()
    with tempfile.TemporaryDirectory(prefix='nex-root-account-') as directory:
        temp = Path(directory)
        ask = temp / 'user-setup-ask'
        confmodule = temp / 'confmodule'
        functions = temp / 'functions.sh'
        confmodule.write_text(MOCK_DEBCONF)
        functions.write_text(helpers)
        isolated = native.replace('/usr/share/debconf/confmodule', str(confmodule)).replace(
            '/usr/lib/user-setup/functions.sh', str(functions))
        report = '\nprintf "RESULT %s %s %s\\n" "$root_login" "$make_user" "$attempts"\n'
        ask.write_text(isolated + report)
        environment = {**os.environ, 'PATH': str(temp) + os.pathsep + os.environ['PATH'],
                       'NEX_TEST_DIR': str(temp)}

        def run(scenario):
            return subprocess.run(['sh', str(ask), str(temp / 'target')],
                                  env={**environment, 'SCENARIO': scenario},
                                  text=True, capture_output=True, timeout=10)

        # Establish that the actual Debian blank-password fallback violates root-only setup.
        baseline = run('blank')
        assert baseline.returncode == 91 and 'REGULAR USER PROMPT' in baseline.stdout, baseline
        ask.rename(temp / 'package-user-setup-ask')
        stubs = {
            'anna': 'test "$*" = "cdrom-retriever default"\ntouch "$NEX_TEST_DIR/anna-ready"',
            'anna-install': 'test "$*" = user-setup-udeb\ntest -f "$NEX_TEST_DIR/anna-ready"\n'
                            'cp "$NEX_TEST_DIR/package-user-setup-ask" "$NEX_TEST_DIR/user-setup-ask"',
            'depmod': 'test \"$*\" = -a',
            'udevadm': 'test \"$*\" = \"control --reload\"',
            'update-dev': ':',
            'debconf-loadtemplate': ':',
        }
        for command, body in stubs.items():
            stub = temp / command
            stub.write_text('#!/bin/sh\nset -eu\n' + body + '\n')
            stub.chmod(0o755)
        hook = (repo / 'installer/root-account.sh').read_text().replace(
            '/usr/bin/user-setup-ask', str(ask)).replace(
            '/usr/share/debconf/confmodule', str(confmodule)).replace(
            '/tmp/nex-root-password.templates', str(temp / 'root-password.templates')).replace(
            '/cdrom/nex/', str(repo / 'installer') + '/')
        subprocess.run(['sh', '-eu', '-c', hook], env=environment, check=True,
                       text=True, capture_output=True, timeout=10)
        assert (temp / 'anna-ready').is_file(), 'Initialize native media loader before installing udeb'
        patched = ask.read_text()
        subprocess.run(['sh', '-eu', '-c', hook], env=environment, check=True,
                       text=True, capture_output=True, timeout=10)
        assert ask.read_text() == patched, 'Repeated hook changed the account script'
        for scenario, attempts, error in [('valid', 1, None), ('blank', 2, 'password-empty'),
                                          ('mismatch', 2, 'password-mismatch')]:
            result = run(scenario)
            assert result.returncode == 0, result
            assert f'RESULT true false {attempts}' in result.stdout, result.stdout
            assert 'REGULAR USER PROMPT' not in result.stdout, result.stdout
            assert result.stdout.count('PROMPT passwd/root-password\n') == attempts, result.stdout
            assert result.stdout.count('PROMPT passwd/root-password-again\n') == attempts, result.stdout
            if error:
                assert f'PROMPT user-setup/{error}' in result.stdout, result.stdout
        ask.write_text(isolated.replace('db_set passwd/root-login false', ':'))
        changed_upstream = subprocess.run(['sh', '-eu', '-c', hook], env=environment,
                                          text=True, capture_output=True, timeout=10)
        assert changed_upstream.returncode != 0, 'Unknown upstream script must fail closed'
        print('PASS: native blank fallback reproduced; root-only valid, blank/mismatch retries; '
              'hook idempotency and upstream-change rejection')


if __name__ == '__main__':
    main()
