#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Install/check/remove only inside a fresh disposable Debian 13 Docker container.
set -euo pipefail
deb=
docker_command=docker
while (($#)); do
    case "$1" in
        --deb) deb=${2:?--deb requires the explicitly selected package}; shift 2 ;;
        --docker) docker_command=${2:?--docker requires an executable or wrapper}; shift 2 ;;
        *) echo 'Usage: core-local-package.sh --deb PACKAGE.deb [--docker EXECUTABLE]' >&2; exit 2 ;;
    esac
done
[[ -n "$deb" && -f "$deb" && "$deb" = *.deb ]] || { echo 'Select an existing .deb using --deb' >&2; exit 2; }
deb=$(readlink -f "$deb")
stage=$(mktemp -d -t nex-package-smoke-XXXXXXXX)
trap 'rm -rf "$stage"' EXIT
cat > "$stage/inside.sh" <<'DISPOSABLE_CHECK'
#!/bin/bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
umask 077
apt-get update
apt-get install -y --no-install-recommends /input/nex.deb
systemctl is-enabled nex-core-local.service
/usr/lib/nex/host-recovery --help >/dev/null
# Seed legacy package files to verify upgrade cleanup; no live host is mounted.
install -d -m700 /var/lib/nex/core-local
printf obsolete > /var/lib/nex/core-local/initial-password
printf '{}' > /var/lib/nex/core-local/credentials.json
/usr/lib/nex/core-local-setup init
test ! -e /var/lib/nex/core-local/initial-password
test ! -e /var/lib/nex/core-local/credentials.json
/usr/lib/nex/core-local-setup run >/tmp/local-service.log 2>&1 &
daemon=$!
trap 'kill "$daemon" 2>/dev/null || true' EXIT
python3 - <<'PY'
import http.client,http.cookiejar,json,ssl,time,urllib.request,subprocess
from pathlib import Path
config=json.loads(Path('/etc/nex/core-local.env').read_text())
origin='https://'+config['address']+':9443'
context=ssl.create_default_context(cafile='/var/lib/nex/core-local/tls.crt')
client=urllib.request.build_opener(urllib.request.ProxyHandler({}),urllib.request.HTTPSHandler(context=context),urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))
for attempt in range(100):
    try:
        assert json.load(client.open(origin+'/healthz',timeout=2))['status']=='ok'
        break
    except OSError:
        time.sleep(.1)
else:
    raise AssertionError('packaged service did not start')
assert 'Local host' in client.open(origin).read().decode()
password='disposable-package-root-password'
rotated='disposable-package-rotated-password'
def set_password(value):
    subprocess.run(['chpasswd'],input=('root:'+value+'\n').encode(),check=True)
def login(username,value,expected):
    # Separate real loopback peers keep account-policy assertions independent of throttling.
    login.peer+=1
    connection=http.client.HTTPSConnection(config['address'],9443,context=context,source_address=(f'127.0.0.{login.peer}',0))
    connection.request('POST','/api/local/v1/session',body=json.dumps({'username':username,'password':value}),headers={'Content-Type':'application/json','Origin':origin})
    response=connection.getresponse()
    body=json.loads(response.read())
    assert response.status==expected,(username,response.status,body)
    if expected==200:
        assert body['username']=='root'
        client.addheaders=[('Cookie',response.getheader('Set-Cookie').split(';')[0])]
    else:
        assert body=={'error':'invalid credentials'}
    connection.close()
login.peer=1
set_password(password)
login('admin',password,401)
login('root','wrong-password',401)
login('root',password,200)
assert json.load(client.open(origin+'/api/local/v1/session'))['username']=='root'
set_password(rotated)
login('root',password,401)
login('root',rotated,200)
subprocess.run(['passwd','-l','root'],check=True,stdout=subprocess.DEVNULL)
login('root',rotated,401)
subprocess.run(['passwd','-u','root'],check=True,stdout=subprocess.DEVNULL)
subprocess.run(['chage','-E','1','root'],check=True)
login('root',rotated,401)
subprocess.run(['chage','-E','-1','root'],check=True)
subprocess.run(['chage','-d','0','root'],check=True)
login('root',rotated,401)
set_password(rotated)
login('root',rotated,200)
host=json.load(client.open(origin+'/api/local/v1/host'))
assert host['defaults']=={'cpus':2,'memory_mib':4096,'disk_gib':32}
assert host['preflight']['ready'] is False
assert 'configure an existing management bridge' in host['preflight']['blockers']
assert 'KVM is unavailable' in host['preflight']['blockers']
assert client.open(origin+'/source').read(2)==b'\x1f\x8b'
assert client.open(origin+'/api/docs/').status==200
assert all(value not in Path('/tmp/local-service.log').read_text() for value in (password,rotated))
assert all(value not in Path('/etc/motd.d/50-nex-local').read_text() for value in (password,rotated))
Path('/var/lib/libvirt/images/preserve-test').write_text('disposable sentinel')
print('PASS installed Debian13 binary, TLS IP verification, root PAM login/session, password rotation, old admin/wrong/locked/account-expired/password-expired rejection, blocked host preflight, resource defaults, host GUI root, source/API docs, no password in logs')
PY
kill "$daemon"
wait "$daemon" || true
trap - EXIT
dpkg --remove nex-core-local
test ! -e /var/lib/nex/core-local/credentials.json
test ! -e /var/lib/nex/core-local/initial-password
test -f /var/lib/libvirt/images/preserve-test
test -f /etc/nex/core-local.env
printf 'PASS package removal retained configuration and disposable storage sentinel without legacy credentials\n'
DISPOSABLE_CHECK
"$docker_command" run --rm --entrypoint /bin/bash \
    -v "$deb:/input/nex.deb:ro" -v "$stage/inside.sh:/input/smoke.sh:ro" \
    debian:13 /input/smoke.sh
