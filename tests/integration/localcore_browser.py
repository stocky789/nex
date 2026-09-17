# SPDX-License-Identifier: AGPL-3.0-only
"""Real HTTPS/API browser acceptance; VM commands are explicit local simulations.

Build nex-core-local and web/dist first; Docker creates disposable Debian PAM state.
--disposable-full needs existing readable KVM, a bridge and >=36GiB free local
ext4/XFS/Btrfs storage; VM commands remain simulated. --blocked-only exercises
portable authentication, blockers, error recovery and responsive behavior.
"""
import argparse
import hashlib
import json
import os
import re
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import time
import urllib.request

from web_security import browser_security, peer_throttle

ROOT = Path(__file__).resolve().parents[2]
PASSWORD = 'disposable-browser-password'
FIXTURE = r'''#!/usr/bin/env python3
import json,os,sys,time
from pathlib import Path
import xml.etree.ElementTree as ET
root=Path(os.environ['NEX_BROWSER_FIXTURE'])
args=sys.argv[1:];name=Path(sys.argv[0]).name
if name=='qemu-img':
 if args[0]=='info': print(json.dumps({'format':'qcow2'}))
 elif args[0]=='resize': time.sleep(3)
 else: sys.exit(2)
elif name=='genisoimage': Path(args[args.index('-output')+1]).write_bytes(b'SIMULATED SEED')
elif name=='virsh':
 assert args[:2]==['--connect','qemu:///session'];args=args[2:]
 cmd=args[0]
 if (root/'inventory-failure').exists() and cmd=='list': sys.exit(1)
 if cmd=='list':
  if (root/'domain.xml').exists(): print(ET.parse(root/'domain.xml').getroot().findtext('uuid'))
 elif cmd=='define':
  (root/'domain.xml').write_text(Path(args[1]).read_text());(root/'power').write_text('shut off')
 elif cmd=='dumpxml': print((root/'domain.xml').read_text())
 elif cmd=='domname': print(ET.parse(root/'domain.xml').getroot().findtext('name'))
 elif cmd=='domstate': print((root/'power').read_text())
 elif cmd=='domjobinfo': print('Job type: None')
 elif cmd=='autostart': pass
 elif cmd=='start':
  if (root/'fail-start').exists(): sys.exit(1)
  (root/'power').write_text('running')
 elif cmd in ['shutdown','destroy']: (root/'power').write_text('shut off')
 else: sys.exit(2)
else: sys.exit(2)
'''

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group(required=True)
    mode.add_argument('--blocked-only', action='store_true')
    mode.add_argument('--disposable-full', action='store_true', help='explicitly allow KVM open/close preflight for simulated VM commands')
    parser.add_argument('--docker', default='docker')
    parser.add_argument('--binary', default=str(ROOT / 'dist/nex-core-local'))
    parser.add_argument('--ui-dir', default=str(ROOT / 'web/dist'))
    parser.add_argument('--captures', default=str(ROOT / '.cache/localcore-browser-captures'))
    args = parser.parse_args()
    args.binary = str(Path(args.binary).resolve())
    from playwright.sync_api import sync_playwright, expect
    captures = Path(args.captures); captures.mkdir(parents=True, exist_ok=True)
    artifact_hash = hashlib.sha256(Path(args.binary).read_bytes()).hexdigest()
    (ROOT / '.cache').mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='localcore-browser-', dir=ROOT / '.cache') as tmp:
        lab = Path(tmp); bins = lab/'bin'; bins.mkdir(); storage=lab/'storage'; storage.mkdir()
        for name in ['virsh','qemu-img','genisoimage']:
            p=bins/name;p.write_text(FIXTURE);p.chmod(0o700)
        (lab/'source.tar.gz').write_bytes(b'QA fixture only; not a distributable source archive')
        (lab/'image').write_bytes(b'SIMULATED QCOW2; never supplied to real QEMU')
        manifest={'format_version':1,'protocol_version':1,'architecture':'amd64','format':'qcow2','version':'browser-simulation','image_sha256':hashlib.sha256((lab/'image').read_bytes()).hexdigest(),'minimum_vcpus':2,'minimum_memory_mib':4096,'minimum_disk_gib':32}
        (lab/'manifest').write_text(json.dumps(manifest))
        def run(*cmd):
            subprocess.run(cmd,check=True,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
        run('openssl','req','-x509','-newkey','rsa:2048','-nodes','-keyout',str(lab/'key'),'-out',str(lab/'cert'),'-days','1','-subj','/CN=localhost','-addext','subjectAltName=DNS:localhost,IP:127.0.0.1')
        run('openssl','genpkey','-algorithm','ED25519','-out',str(lab/'signer'))
        pub=subprocess.check_output(['openssl','pkey','-in',str(lab/'signer'),'-pubout','-outform','DER'])
        (lab/'pub').write_bytes(pub[-32:])
        run('openssl','pkeyutl','-sign','-rawin','-inkey',str(lab/'signer'),'-in',str(lab/'manifest'),'-out',str(lab/'signature'))
        (lab/'fail-start').touch()
        with socket.socket() as sock:
            sock.bind(('127.0.0.1',0));port=sock.getsockname()[1]
        url=f'https://127.0.0.1:{port}'
        bridges=[p.name for p in Path('/sys/class/net').iterdir() if (p/'bridge').exists()]
        bridge='qa-missing' if args.blocked_only else (bridges[0] if bridges else 'qa-missing')
        # Authentication runs against a disposable Debian root account, never this host's PAM.
        dockerfile = 'FROM debian:13\nRUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev libpam0g-dev libpam-modules python3 iproute2 && rm -rf /var/lib/apt/lists/*\n'
        subprocess.run([args.docker,'build','-t','nex-local-browser-pam','-'],input=dockerfile.encode(),check=True,stdout=subprocess.DEVNULL)
        container='nex-browser-'+lab.name
        env=os.environ.copy()
        command=[args.binary,'--listen',f'127.0.0.1:{port}','--data-dir',str(lab/'state'),'--agent-state-dir',str(lab/'agent'),'--tls-cert',str(lab/'cert'),'--tls-key',str(lab/'key'),'--source-archive',str(lab/'source.tar.gz'),'--ui-dir',str(Path(args.ui_dir).resolve()),'--storage-dir',str(storage),'--bridge',bridge,'--image',str(lab/'image'),'--manifest',str(lab/'manifest'),'--signature',str(lab/'signature'),'--trusted-key',str(lab/'pub'),'--libvirt-uri','qemu:///session']
        startup = 'set -eu; cc -Wall -Wextra -Werror "$1/packaging/core-local/nex-root-auth.c" -lpam -o /tmp/nex-root-auth; mkdir -p /usr/lib/nex; install -m755 /tmp/nex-root-auth /usr/lib/nex/nex-root-auth; install -m644 "$1/packaging/core-local/nex-core-local.pam" /etc/pam.d/nex-core-local; chpasswd; shift; exec "$@"'
        command=[args.docker,'run','--rm','-i','--name',container,'--network','host','--hostname',socket.gethostname(),'-v',f'{ROOT}:{ROOT}:ro','-v',f'{args.binary}:{args.binary}:ro','-v',f'{lab}:{lab}', '-e',f'NEX_BROWSER_FIXTURE={lab}','-e',f'PATH={bins}:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin', *(['--device','/dev/kvm:/dev/kvm:rw'] if args.disposable_full else []),'nex-local-browser-pam','sh','-c',startup,'sh',str(ROOT),*command]
        def start(log):
            process=subprocess.Popen(command,env=env,stdin=subprocess.PIPE,stdout=log,stderr=log)
            process.stdin.write(('root:'+PASSWORD+'\n').encode());process.stdin.close()
            return process
        def stop(process):
            subprocess.run([args.docker,'exec',container,'chown','-R',f'{os.getuid()}:{os.getgid()}',str(lab)],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            subprocess.run([args.docker,'stop','-t','5',container],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
            process.wait(timeout=15)
        with (lab/'service.log').open('w') as log:
            process=start(log)
            try:
                for _ in range(100):
                    if process.poll() is not None: raise AssertionError((lab/'service.log').read_text())
                    try:
                        urllib.request.urlopen(url+'/healthz',context=ssl._create_unverified_context(),timeout=.5).close();break
                    except OSError: time.sleep(.1)
                else: raise AssertionError('local HTTPS service did not start')
                with sync_playwright() as pw:
                    browser=pw.chromium.launch(headless=True)
                    page=browser.new_page(ignore_https_errors=True,viewport={'width':1440,'height':1100})
                    errors=[];page.on('pageerror',lambda error:errors.append(str(error)))
                    def capture(name): page.screenshot(path=str(captures/(name+'.png')),full_page=True)
                    held=[]
                    page.route('**/api/local/v1/session',lambda route:held.append(route))
                    initial_response=page.goto(url);expect(page.get_by_role('heading',name='Connecting to Nex Core…')).to_be_visible();capture('loading')
                    for route in held: route.continue_()
                    page.unroute('**/api/local/v1/session')
                    expect(page.get_by_label('Username',exact=True)).to_have_value('root')
                    assert page.get_by_label('Username',exact=True).get_attribute('readonly') is not None
                    expect(page.get_by_text('Use the root password set during installation.',exact=True)).to_be_visible()
                    expect(page.get_by_label('Password',exact=True)).to_be_focused()
                    capture('root-login')
                    page.keyboard.type('wrong-password');page.keyboard.press('Enter')
                    expect(page.get_by_role('alert')).to_be_visible();capture('invalid-login')
                    page.wait_for_timeout(1100)
                    page.get_by_label('Password',exact=True).fill(PASSWORD);page.keyboard.press('Enter')
                    expect(page.get_by_role('heading',name='No virtual machines observed')).to_be_visible();capture('empty-desktop')
                    assert page.get_by_text(re.compile(r'^Observed \d')).count()==0
                    browser_security(page,initial_response,'__Host-nex_local_session',url+'/api/local/v1/host')
                    # Form-only API fixtures run on every CI runner; PAM/session remain real.
                    # No VM deployment is attempted by this UI contract check.
                    simulated_host={'hostname':'form-fixture','bridge':'nexbr0','storage_dir':'/var/lib/libvirt/images','defaults':{'cpus':2,'memory_mib':4096,'disk_gib':32},'preflight':{'ready':True,'blockers':[],'cpus':8,'memory_mib':16384,'disk_gib':100}}
                    page.route('**/api/local/v1/host',lambda route:route.fulfill(json=simulated_host))
                    page.get_by_role('button',name='Refresh',exact=True).click()
                    page.get_by_role('button',name='Deploy Controller').click()
                    expect(page.get_by_text('Network settings',exact=True)).to_be_visible()
                    mode=page.get_by_label('IP configuration',exact=True)
                    expect(mode).to_have_value('dhcp')
                    expect(page.get_by_label('IPv4 address / prefix',exact=True)).to_have_count(0)
                    page.get_by_role('button',name='Review deployment').click()
                    expect(page.get_by_role('heading',name='Deploy Nex Controller',exact=True)).to_be_visible()
                    expect(page.get_by_label('Local storage')).to_have_value(simulated_host['storage_dir'])
                    expect(page.get_by_label('Local storage')).to_be_disabled()
                    page.get_by_label('LAN bridge').select_option('nexbr0')
                    new_password=page.get_by_label('Controller administrator password',exact=True)
                    for accepted in ['x','😀','  ']:
                        new_password.fill(accepted)
                        assert new_password.evaluate('(input) => input.validity.valid'), 'nonempty short password rejected'
                    new_password.fill('é'*513)
                    assert not new_password.evaluate('(input) => input.validity.valid'), 'oversized password accepted'
                    new_password.fill('x')
                    mode.select_option('static')
                    page.get_by_role('button',name='Review deployment').click()
                    expect(page.get_by_role('heading',name='Deploy Nex Controller',exact=True)).to_be_visible()
                    for title,value in [('IPv4 address / prefix','192.0.2.20/24'),('IPv4 gateway','192.0.2.1'),('DNS servers','192.0.2.1')]: page.get_by_label(title,exact=True).fill(value)
                    capture('deployment-static-desktop')
                    page.get_by_role('button',name='Review deployment').click()
                    expect(page.locator('.review')).to_contain_text('Static IPv4')
                    expect(page.locator('.review')).to_contain_text('192.0.2.20/24')
                    static_payloads=[]
                    def reject_static(route):
                        if route.request.method=='POST':
                            static_payloads.append(route.request.post_data_json)
                            route.fulfill(status=400,json={'error':'Simulated static rejection'})
                        else: route.continue_()
                    page.route('**/api/local/v1/deployments',reject_static)
                    page.get_by_role('button',name='Create VM & install Controller').click()
                    expect(page.get_by_role('alert')).to_contain_text('Simulated static rejection')
                    assert len(static_payloads)==1 and static_payloads[0]['network_mode']=='static'
                    assert static_payloads[0]['address_cidr']=='192.0.2.20/24' and static_payloads[0]['gateway']=='192.0.2.1' and static_payloads[0]['dns']==['192.0.2.1']
                    assert static_payloads[0]['admin_password']=='x'
                    page.unroute('**/api/local/v1/deployments')
                    page.get_by_role('button',name='Retry connection',exact=True).click()
                    expect(page.get_by_role('alert')).to_have_count(0)
                    new_password.fill('x')
                    mode.select_option('dhcp')
                    capture('deployment-dhcp-desktop')
                    page.set_viewport_size({'width':360,'height':900})
                    capture('deployment-dhcp-mobile')
                    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'), 'deployment overflows narrow viewport'
                    page.get_by_role('button',name='Review deployment').focus();page.keyboard.press('Enter')
                    expect(page.locator('.review')).to_contain_text('Automatic (DHCP)')
                    expect(page.locator('.review')).not_to_contain_text('192.0.2.20/24')
                    assert page.locator('.review input[type=password]').count()==0
                    capture('deployment-dhcp-review-mobile')
                    submitted=[]
                    def reject_form_deploy(route):
                        if route.request.method=='POST':
                            submitted.append(route.request.post_data_json)
                            route.fulfill(status=400,json={'error':'Simulated deployment rejection'})
                        else: route.continue_()
                    page.route('**/api/local/v1/deployments',reject_form_deploy)
                    page.get_by_role('button',name='Create VM & install Controller').click()
                    expect(page.get_by_role('alert')).to_contain_text('Simulated deployment rejection')
                    assert len(submitted)==1
                    assert submitted[0]['network_mode']=='dhcp' and submitted[0]['address_cidr']=='' and submitted[0]['gateway']=='' and submitted[0]['dns']==[]
                    assert submitted[0]['admin_password']=='x'
                    expect(new_password).to_have_value('')
                    capture('deployment-submit-error-mobile')
                    page.unroute('**/api/local/v1/deployments')
                    page.unroute('**/api/local/v1/host')
                    page.set_viewport_size({'width':1440,'height':1100})
                    page.reload()
                    expect(page.get_by_role('heading',name='No virtual machines observed')).to_be_visible()
                    # Explicit power confirmation is a button, without hostname transcription.
                    simulated_vm={'id':'fixture-vm','name':'Controller fixture','state':'running','managed':True,'power_allowed':True}
                    page.route('**/api/local/v1/vms',lambda route:route.fulfill(json=[simulated_vm]))
                    page.get_by_role('button',name='Refresh',exact=True).click()
                    power_requests=[]
                    def accept_power(route):
                        power_requests.append(route.request.post_data_json)
                        route.fulfill(json={'id':'fixture-operation','status':'running'})
                    page.route('**/api/local/v1/vms/fixture-vm/actions',accept_power)
                    for button_name,action_name in [('Start','start'),('Shut down','shutdown'),('Force stop','force-stop')]:
                        page.get_by_role('button',name=button_name,exact=True).click()
                        region=page.get_by_role('region',name='Confirm VM power action')
                        expect(region).to_contain_text('Controller fixture')
                        expect(region.get_by_role('textbox')).to_have_count(0)
                        assert len(power_requests)==0
                        page.get_by_role('button',name='Cancel power action',exact=True).click()
                        expect(region).to_have_count(0)
                        page.get_by_role('button',name=button_name,exact=True).click()
                        if action_name=='force-stop':
                            expect(region).to_contain_text('may lose unwritten data')
                            capture('power-confirmation-desktop')
                            page.set_viewport_size({'width':360,'height':900});capture('power-confirmation-mobile')
                        region.get_by_role('button',name='Confirm '+action_name.replace('-',' '),exact=True).focus()
                        page.keyboard.press('Enter')
                        expect(region).to_have_count(0)
                        assert len(power_requests)==1 and power_requests[0]['action']==action_name
                        assert 'confirmation' not in power_requests[0]
                        power_requests.clear()
                    page.unroute('**/api/local/v1/vms/fixture-vm/actions')
                    # Every row's console action stays reachable at 360px without sideways table scrolling.
                    other_vm={'id':'fixture-other','name':'Other fixture','state':'running','managed':False,'power_allowed':False,'blocked_reason':'Local power control is limited to the Controller appliance deployed on this host'}
                    page.route('**/api/local/v1/vms',lambda route:route.fulfill(json=[simulated_vm,other_vm]))
                    page.set_viewport_size({'width':360,'height':800})
                    page.get_by_role('button',name='Refresh',exact=True).click()
                    consoles=page.get_by_role('button',name='Open console',exact=True)
                    expect(consoles).to_have_count(2)
                    capture('vm-table-360')
                    scroller=page.locator('.table-scroll').bounding_box()
                    for index in range(2):
                        box=consoles.nth(index).bounding_box()
                        assert box['x']>=scroller['x'] and box['x']+box['width']<=scroller['x']+scroller['width']+0.5, ('console button outside table at 360px',box,scroller)
                    assert page.locator('.table-scroll').evaluate('el => el.scrollWidth <= el.clientWidth'), 'VM table scrolls sideways at 360px'
                    page.unroute('**/api/local/v1/vms')
                    page.set_viewport_size({'width':1440,'height':1100});page.reload()
                    expect(page.get_by_role('heading',name='No virtual machines observed')).to_be_visible()
                    if args.blocked_only:
                        expect(page.get_by_role('heading',name='Deployment needs attention')).to_be_visible()
                        expect(page.get_by_role('button',name='Deploy Controller')).to_be_disabled();capture('missing-bridge')
                    else:
                        button=page.get_by_role('button',name='Deploy Controller')
                        expect(button).to_be_enabled(timeout=10000)
                        button.click()
                        page.get_by_role('button',name='Review deployment').click()
                        expect(page.get_by_role('heading',name='Deploy Nex Controller',exact=True)).to_be_visible()
                        expect(page.get_by_label('Local storage')).to_have_value(str(storage));page.get_by_label('LAN bridge').select_option(bridge)
                        page.get_by_label('IP configuration',exact=True).select_option('static')
                        for label,value in [('IPv4 address / prefix','192.0.2.20/24'),('IPv4 gateway','192.0.2.1'),('DNS servers','192.0.2.1'),('Controller administrator password','x')]: page.get_by_label(label,exact=True).fill(value)
                        page.get_by_role('button',name='Review deployment').click()
                        expect(page.get_by_role('heading',name='Review your appliance')).to_be_visible()
                        assert page.locator('.review input[type=password]').count()==0
                        capture('deployment-review-desktop')
                        # Lose the real POST response after the service commits durable intent.
                        def lose_response(route):
                            if route.request.method=='POST': route.fetch();route.abort()
                            else: route.continue_()
                        page.route('**/api/local/v1/deployments',lose_response)
                        page.get_by_role('button',name='Create VM & install Controller').click()
                        expect(page.get_by_role('alert')).to_contain_text('Check deployment progress')
                        page.unroute('**/api/local/v1/deployments');page.reload()
                        expect(page.locator('.operation')).to_have_count(1)
                        expect(page.locator('.operation')).to_contain_text('running',timeout=5000);capture('durable-progress')
                        expect(page.locator('.operation')).to_contain_text('needs attention',timeout=15000);capture('deployment-start-failure')
                        page.get_by_role('button',name='Retry deployment',exact=True).click()
                        expect(page.locator('.operation')).to_have_count(1)
                        # The fixture now permits explicit recovery; the actual API/engine journals each action.
                        (lab/'fail-start').unlink()
                        page.get_by_role('button',name='Start',exact=True).click()
                        expect(page.get_by_role('region',name='Confirm VM power action').get_by_role('textbox')).to_have_count(0)
                        page.get_by_role('button',name='Confirm start',exact=True).click()
                        expect(page.locator('tbody')).to_contain_text('running')
                        page.get_by_role('button',name='Force stop',exact=True).click()
                        expect(page.get_by_role('button',name='Confirm force stop',exact=True)).to_be_enabled();capture('power-confirmation')
                        page.get_by_role('button',name='Confirm force stop',exact=True).click()
                        expect(page.locator('tbody')).to_contain_text('shut off')
                        page.reload();expect(page.locator('.operation')).to_have_count(1)
                    page.set_viewport_size({'width':360,'height':800});capture('overview-360')
                    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth'),'360px horizontal overflow'
                    # Actual subprocess inventory failure; no fabricated HTTP response.
                    (lab/'inventory-failure').touch();page.get_by_role('button',name='Refresh',exact=True).click()
                    expect(page.get_by_role('status')).to_contain_text('stale')
                    expect(page.get_by_role('button',name='Deploy Controller')).to_be_disabled()
                    if not args.blocked_only: expect(page.get_by_role('button',name='Force stop',exact=True)).to_be_disabled()
                    capture('inventory-error-360')
                    (lab/'inventory-failure').unlink();page.get_by_role('button',name='Retry connection').click()
                    expect(page.get_by_role('alert')).to_have_count(0)
                    # Local sessions live in memory; restart genuinely invalidates this session.
                    # This exercises session-loss login recovery, not 30 minutes of idle time.
                    stop(process)
                    process=start(log)
                    for _ in range(100):
                        try:
                            urllib.request.urlopen(url+'/healthz',context=ssl._create_unverified_context(),timeout=.5).close();break
                        except OSError: time.sleep(.1)
                    else: raise AssertionError('local service restart failed: '+(lab/'service.log').read_text()[-2000:])
                    page.reload()
                    expect(page.get_by_label('Password',exact=True)).to_be_visible();capture('session-lost-login')
                    page.get_by_label('Password',exact=True).fill(PASSWORD)
                    page.get_by_role('button',name='Sign in',exact=True).click()
                    expect(page.get_by_role('button',name='Sign out',exact=True)).to_be_visible()
                    page.get_by_role('button',name='Sign out',exact=True).focus();page.keyboard.press('Enter')
                    expect(page.get_by_label('Password',exact=True)).to_be_visible()
                    assert not any(c['name']=='__Host-nex_local_session' for c in page.context.cookies()), 'logout retained local cookie'
                    assert page.request.get(url+'/api/local/v1/host').status==401
                    peer_throttle(page,url+'/api/local/v1/session')
                    assert not errors,errors
                    browser.close()
                (captures/'results.json').write_text(json.dumps({'result':'passed','mode':'blocked-only' if args.blocked_only else 'full-simulated-vm','binary_sha256':artifact_hash,'ui_sha256':{p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in sorted(Path(args.ui_dir).glob('local.*'))},'vm_commands':'simulated virsh/qemu-img/genisoimage; real HTTPS, Debian13 root PAM in disposable container, storage journals and browser','unix_time':time.time()},indent=2)+'\n')
                print('PASS: real localcore HTTPS login/CSRF browser flow, empty inventory, durable API state, honest errors/recovery, desktop/360 keyboard; VM commands SIMULATED'+(' (blocked-only)' if args.blocked_only else ' (deployment response loss, start failure, explicit start/force-stop)'))
            finally:
                stop(process)

if __name__=='__main__': main()
