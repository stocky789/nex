#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Simulated startup: check private answer import and fail-closed disk hook."""
from pathlib import Path
import tempfile,subprocess,os
def startup(p,config,wizard=''):
 """Run start.sh in fixture root p with stubbed installer commands."""
 (p/'run/nex-installer').mkdir(parents=True); (p/'var/lib/preseed').mkdir(parents=True); (p/'var/log').mkdir(parents=True); (p/'bin').mkdir()
 (p/'var/lib/preseed/log').write_text('old diagnostic\n')
 s=Path('installer/gui/start.sh').read_text().replace('/run/nex-installer',str(p/'run/nex-installer')).replace('/var/lib/preseed',str(p/'var/lib/preseed')).replace('/var/log/nex-installer-prepare.log',str(p/'var/log/log')).replace('/usr/bin/nex-wizard',str(p/'bin/nex-wizard')).replace('/usr/bin/nex-config',str(p/'bin/nex-config'))
 for name,body in {'debconf-get':'echo false','nex-wizard':f'{wizard}touch {p}/run/nex-installer/prepared','nex-config':config,'debconf-set-selections':f'cat "$1" >> {p}/var/lib/preseed/log'}.items():
  f=p/'bin'/name; f.write_text('#!/bin/sh\n'+body+'\n');f.chmod(0o755)
 env=dict(os.environ,PATH=str(p/'bin')+':'+os.environ['PATH'],DEBIAN_FRONTEND='gtk')
 subprocess.run(['sh','-ec','umask 022\n' + s + '\ntest "$(umask)" = 0022'],env=env,check=True,timeout=5)

with tempfile.TemporaryDirectory() as d:
 p=Path(d); startup(p,f'echo hash-only > {p}/run/nex-installer/answers.cfg')
 assert (p/'var/lib/preseed/log').read_text()=='old diagnostic\n'
 assert not (p/'run/nex-installer/answers.cfg').exists()
 assert (p/'run/nex-installer/active').exists()
 print('PASS: mocked startup handoff imports answers, preserves prior log, does not retain imported hash')

# A rejected apply reaches the next wizard pass as nex-config's own reason, not a fixed string.
with tempfile.TemporaryDirectory() as d:
 p=Path(d); r=p/'run/nex-installer'; reason='Disk serial changed; select the disk again.'
 startup(p,f'[ -e {r}/rejected ] && {{ echo hash-only > {r}/answers.cfg; exit 0; }}\ntouch {r}/rejected; echo noise; echo "{reason}" >&2; echo >&2; exit 1',f'[ -f {r}/error ] && cat {r}/error >> {p}/seen\n')
 assert (p/'seen').read_text()==reason+'\n', (p/'seen').read_text()
 assert reason in (p/'var/log/log').read_text()
 assert not (r/'apply-error').exists() and not (r/'error').exists() and not (r/'answers.cfg').exists()
 print('PASS: rejected apply shows nex-config reason on the next pass, keeps it in the log, leaves no capture file')

with tempfile.TemporaryDirectory() as d:
 p=Path(d); (p/'active').touch()
 command=p/'check';command.write_text('#!/bin/sh\nexit 1\n');command.chmod(0o755)
 s=Path('installer/gui/recheck.sh').read_text().replace('/run/nex-installer/active',str(p/'active')).replace('/usr/bin/nex-config',str(command))
 assert subprocess.run(['sh','-c',s],capture_output=True).returncode != 0
 (p/'active').unlink()
 assert subprocess.run(['sh','-c',s],capture_output=True).returncode == 0
 print('PASS: direct partition hook fails closed while Nex setup is active and is a no-op when it is not')
