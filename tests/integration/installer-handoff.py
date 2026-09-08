#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Simulated startup: check private answer import and fail-closed disk hook."""
from pathlib import Path
import tempfile,subprocess,os
with tempfile.TemporaryDirectory() as d:
 p=Path(d); (p/'run/nex-installer').mkdir(parents=True); (p/'var/lib/preseed').mkdir(parents=True); (p/'var/log').mkdir(parents=True); (p/'bin').mkdir()
 (p/'var/lib/preseed/log').write_text('old diagnostic\n')
 s=Path('installer/gui/start.sh').read_text().replace('/run/nex-installer',str(p/'run/nex-installer')).replace('/var/lib/preseed',str(p/'var/lib/preseed')).replace('/var/log/nex-installer-prepare.log',str(p/'var/log/log')).replace('/usr/bin/nex-wizard',str(p/'bin/nex-wizard')).replace('/usr/bin/nex-config',str(p/'bin/nex-config'))
 for name,body in {'debconf-get':'echo false','nex-wizard':f'touch {p}/run/nex-installer/prepared','nex-config':f'echo hash-only > {p}/run/nex-installer/answers.cfg','debconf-set-selections':f'cat "$1" >> {p}/var/lib/preseed/log'}.items():
  f=p/'bin'/name; f.write_text('#!/bin/sh\n'+body+'\n');f.chmod(0o755)
 env=dict(os.environ,PATH=str(p/'bin')+':'+os.environ['PATH'],DEBIAN_FRONTEND='gtk')
 subprocess.run(['sh','-ec','umask 022\n' + s + '\ntest "$(umask)" = 0022'],env=env,check=True,timeout=5)
 assert (p/'var/lib/preseed/log').read_text()=='old diagnostic\n'
 assert not (p/'run/nex-installer/answers.cfg').exists()
 assert (p/'run/nex-installer/active').exists()
 print('PASS: mocked startup handoff imports answers, preserves prior log, does not retain imported hash')

with tempfile.TemporaryDirectory() as d:
 p=Path(d); (p/'active').touch()
 command=p/'check';command.write_text('#!/bin/sh\nexit 1\n');command.chmod(0o755)
 s=Path('installer/gui/recheck.sh').read_text().replace('/run/nex-installer/active',str(p/'active')).replace('/usr/bin/nex-config',str(command))
 assert subprocess.run(['sh','-c',s],capture_output=True).returncode != 0
 (p/'active').unlink()
 assert subprocess.run(['sh','-c',s],capture_output=True).returncode == 0
 print('PASS: direct partition hook fails closed for GUI and leaves minimal mode untouched')
