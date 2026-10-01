#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Simulated startup: check private answer import and fail-closed disk hook."""
from pathlib import Path
import tempfile,subprocess,os,shutil
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

# BUG-0413: the selected disk held one member of a four-disk RAID from a previous install.
# Only that disk is cleared (partitions and table); the array is stopped, never modified.
with tempfile.TemporaryDirectory() as d:
 p=Path(d); (p/'active').touch(); MiB=1024*1024
 (p/'check').write_text('#!/bin/sh\nexit 0\n'); (p/'check').chmod(0o755)
 (p/'bin').mkdir(); (p/'bin/mdadm').write_text(f'#!/bin/sh\necho "$@" >> {p}/mdadm.log\n'); (p/'bin/mdadm').chmod(0o755)
 (p/'config.ini').write_text('[Host]\ndisk=/dev/sda\n')
 (p/'mdstat').write_text('Personalities : [raid1]\nmd127 : active raid1 sdb3[1] sda3[0] sdc3[2] sdd3[3]\nmd126 : active raid1 sdb1[1] sdc1[0]\n')
 layout={'sda':[('sda1',2048,8192),('sda3',10240,20480)],'sdb':[('sdb3',10240,20480)]}
 for disk,parts in layout.items():
  (p/'dev'/disk).parent.mkdir(exist_ok=True); (p/'dev'/disk).write_bytes(b'\xff'*(32*MiB))
  (p/'sys/class/block'/disk).mkdir(parents=True); (p/'sys/class/block'/disk/'size').write_text(str(32*MiB//512))
  for part,start,size in parts:
   q=p/'sys/class/block'/disk/part; q.mkdir(); (q/'partition').write_text('1'); (q/'size').write_text(str(size))
   (p/'dev'/part).write_bytes(b'\xff'*(size*512))
 s=(Path('installer/gui/recheck.sh').read_text().replace('/run/nex-installer/',f'{p}/').replace('/usr/bin/nex-config',str(p/'check'))
    .replace('SYS=/sys',f'SYS={p}/sys').replace('DEV=/dev',f'DEV={p}/dev').replace('MDSTAT=/proc/mdstat',f'MDSTAT={p}/mdstat').replace('PARTMAN=/var/lib/partman',f'PARTMAN={p}/partman'))
 env=dict(os.environ,PATH=f'{p}/bin:'+os.environ['PATH'])
 r=subprocess.run(['sh','-c',s],capture_output=True,text=True,env=env); assert r.returncode==0, r.stderr
 assert (p/'mdadm.log').read_text()==f'--stop {p}/dev/md127\n', 'Only the array holding the selected disk is stopped'
 assert (p/'partman/md').exists() and (p/'partman/lvm').exists(), 'partman must not reassemble the leftover array'
 for dev in ('sda','sda1','sda3'):
  b=(p/'dev'/dev).read_bytes(); n=min(MiB,len(b)//2)
  assert b[:n]==bytes(n) and b[-n:]==bytes(n), f'{dev} signatures not cleared'
 assert b'\xff' in (p/'dev/sda').read_bytes()[MiB:-MiB], 'Middle of the disk is not rewritten'
 for dev in ('sdb','sdb3'): assert (p/'dev'/dev).read_bytes()==b'\xff'*len((p/'dev'/dev).read_bytes()), f'{dev} was modified'
 (p/'dev/sda').write_bytes(b'\xff'*(32*MiB)); assert subprocess.run(['sh','-c',s],env=env).returncode==0
 assert (p/'dev/sda').read_bytes()[:16]==b'\xff'*16, 'A partman restart must not clear the disk again'
 print('PASS: partition hook clears only the selected RAID/LVM disk, once, and only stops its array')

# Two selected disks: wipe both, stop only their arrays, leave a third array and its disk alone.
with tempfile.TemporaryDirectory() as d:
 p=Path(d); (p/'active').touch(); MiB=1024*1024
 (p/'check').write_text('#!/bin/sh\nexit 0\n'); (p/'check').chmod(0o755)
 (p/'bin').mkdir(); (p/'bin/mdadm').write_text(f'#!/bin/sh\necho "$@" >> {p}/mdadm.log\n'); (p/'bin/mdadm').chmod(0o755)
 (p/'config.ini').write_text('[Host]\ndisk=/dev/sda;/dev/sdb;\n')
 (p/'mdstat').write_text(
  'Personalities : [raid1]\n'
  'md127 : active raid1 sda3[0] sdc3[1]\n'
  'md126 : active raid1 sdb3[0] sdd3[1]\n'
  'md125 : active raid1 sdc1[0] sdd1[1]\n')
 layout={
  'sda':[('sda3',10240,20480)],
  'sdb':[('sdb3',10240,20480)],
  'sdc':[('sdc1',2048,8192),('sdc3',10240,20480)],
 }
 for disk,parts in layout.items():
  (p/'dev'/disk).parent.mkdir(exist_ok=True); (p/'dev'/disk).write_bytes(b'\xff'*(32*MiB))
  (p/'sys/class/block'/disk).mkdir(parents=True); (p/'sys/class/block'/disk/'size').write_text(str(32*MiB//512))
  for part,_,size in parts:
   q=p/'sys/class/block'/disk/part; q.mkdir(); (q/'partition').write_text('1'); (q/'size').write_text(str(size))
   (p/'dev'/part).write_bytes(b'\xff'*(size*512))
 s=(Path('installer/gui/recheck.sh').read_text().replace('/run/nex-installer/',f'{p}/').replace('/usr/bin/nex-config',str(p/'check'))
    .replace('SYS=/sys',f'SYS={p}/sys').replace('DEV=/dev',f'DEV={p}/dev').replace('MDSTAT=/proc/mdstat',f'MDSTAT={p}/mdstat').replace('PARTMAN=/var/lib/partman',f'PARTMAN={p}/partman'))
 env=dict(os.environ,PATH=f'{p}/bin:'+os.environ['PATH'])
 r=subprocess.run(['sh','-c',s],capture_output=True,text=True,env=env); assert r.returncode==0, r.stderr
 stops=sorted((p/'mdadm.log').read_text().splitlines())
 assert stops==[f'--stop {p}/dev/md126', f'--stop {p}/dev/md127'], stops
 for dev in ('sda','sda3','sdb','sdb3'):
  b=(p/'dev'/dev).read_bytes(); n=min(MiB,len(b)//2)
  assert b[:n]==bytes(n) and b[-n:]==bytes(n), f'{dev} signatures not cleared'
  if len(b) > 2*MiB:
   assert b'\xff' in b[MiB:-MiB], f'{dev} middle rewritten'
 for dev in ('sdc','sdc1','sdc3'):
  assert (p/'dev'/dev).read_bytes()==b'\xff'*len((p/'dev'/dev).read_bytes()), f'{dev} was modified'
 before={dev: (p/'dev'/dev).read_bytes() for dev in ('sda','sdb','sdc','sda3','sdb3','sdc1','sdc3')}
 assert subprocess.run(['sh','-c',s],env=env).returncode==0
 for dev,data in before.items():
  assert (p/'dev'/dev).read_bytes()==data, f'second run rewrote {dev}'
 print('PASS: partition hook clears every selected mirror disk, stops only their arrays, and is idempotent')

# Image edition: no partman. The hook clears nothing: it hands the selected disks to nex-image-install.sh
# (preflight first, then install or repair), which clears them only after the image and payloads verify.
# A classic media must never run that script, and the net installer asks for the image rows only.
scratch=[]
def image_hook(mode, netinst=False, edition=True):
 d=tempfile.mkdtemp(); scratch.append(d); p=Path(d); MiB=1024*1024
 (p/'active').touch()
 (p/'check').write_text('#!/bin/sh\nexit 0\n'); (p/'check').chmod(0o755)
 (p/'bin').mkdir(); (p/'bin/mdadm').write_text('#!/bin/sh\nexit 0\n'); (p/'bin/mdadm').chmod(0o755)
 (p/'cdrom/nex').mkdir(parents=True)
 (p/'cdrom/nex/nex-image-install.sh').write_text(f'echo "$1" >> {p}/image.log\n')
 (p/'cdrom/nex/netinst-fetch.sh').write_text(f'echo "rows=$NEX_NETINST_ROWS $*" >> {p}/fetch.log\n')
 if netinst:
  (p/'cdrom/nex/payloads.tsv').write_text('')
  (p/'netinst-resolv.conf').write_text('nameserver 192.0.2.53\n')
 if edition: (p/'image.env').write_text(f'mode={mode}\nlayout=single\ndisks=/dev/sda\n')
 (p/'config.ini').write_text('[Host]\ndisk=/dev/sda\n')
 (p/'mdstat').write_text('Personalities : [raid1]\n')
 (p/'dev').mkdir(); (p/'dev/sda').write_bytes(b'\xff'*(32*MiB))
 (p/'sys/class/block/sda').mkdir(parents=True); (p/'sys/class/block/sda/size').write_text(str(32*MiB//512))
 s=(Path('installer/gui/recheck.sh').read_text().replace('/run/nex-installer/',f'{p}/').replace('/usr/bin/nex-config',str(p/'check'))
    .replace('/cdrom/nex/',f'{p}/cdrom/nex/').replace('/etc/resolv.conf',str(p/'resolv.conf'))
    .replace('SYS=/sys',f'SYS={p}/sys').replace('DEV=/dev',f'DEV={p}/dev').replace('MDSTAT=/proc/mdstat',f'MDSTAT={p}/mdstat').replace('PARTMAN=/var/lib/partman',f'PARTMAN={p}/partman'))
 env=dict(os.environ,PATH=f'{p}/bin:'+os.environ['PATH'])
 r=subprocess.run(['sh','-c',s],capture_output=True,text=True,env=env); assert r.returncode==0, r.stderr
 return p, (p/'image.log').read_text().split() if (p/'image.log').exists() else []

p,calls=image_hook('install')
assert calls==['preflight','install'], calls
assert (p/'dev/sda').read_bytes()==b'\xff'*(32*MiB), 'the hook must not clear a disk before the image installer has verified the image'
assert not list(p.glob('wiped-*')), 'the hook leaves no erase markers for the image edition'
assert not (p/'partman').exists(), 'the image edition has no partman to prepare'
p,calls=image_hook('repair')
assert calls==['preflight','repair'], calls
assert (p/'dev/sda').read_bytes()==b'\xff'*(32*MiB), 'Repair must not clear any disk'
assert not list(p.glob('wiped-*')), 'Repair leaves no erase markers'
p,calls=image_hook('install',edition=False)
assert calls==[], 'classic media must not run the image installer'
assert (p/'partman/md').exists() and (p/'partman/lvm').exists(), 'classic media still prepares partman'
p,calls=image_hook('install',netinst=True)
assert (p/'fetch.log').read_text().strip()=='rows=image/ payload/ --check', (p/'fetch.log').read_text()
assert (p/'resolv.conf').read_text()=='nameserver 192.0.2.53\n'
p,calls=image_hook('install',netinst=True,edition=False)
assert (p/'fetch.log').read_text().strip()=='rows= --check', 'classic net installer keeps its own rows'
for d in scratch: shutil.rmtree(d)
print('PASS: image edition hands untouched disks to the image installer; classic media never does')
