#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Phase 2 gate guest marker and heartbeat, read and written through qemu-guest-agent.

write: create a marker file and its SHA-256 in an installed guest of the selected
disposable fresh-install lab, then start a 1 s heartbeat writer as a transient systemd unit.
heartbeat: (re)start only the heartbeat writer, keeping the marker; a transient unit does not
survive a guest reboot, so run it after every guest or host reboot. It is a no-op while active.
check: read both back, verify the marker hash against the one recorded by write, and report
every heartbeat gap longer than --max-gap seconds inside an optional time window. Without
--until the heartbeat unit must still be active.
Heartbeat samples use the guest clock while --since/--until are workstation epoch times, so every
stage measures the guest clock offset and check shifts the window by it before looking for gaps.
The guest needs qemu-guest-agent running. Only the explicitly supplied VM UUID is touched.
"""
import argparse
import hashlib
import json
from pathlib import Path
import secrets
import shlex
import time
import uuid

DIR = '/var/lib/nex-gate'
UNIT = 'nex-gate-heartbeat'


def heartbeat_gaps(text, max_gap, since=None, until=None):
    """Return (samples, gaps) for heartbeat lines of epoch seconds. A gap is a pair of
    consecutive in-window samples further apart than max_gap; the window edges also count,
    so a writer that stopped before `until` is reported."""
    samples = sorted(float(line) for line in text.split() if line.strip())
    if since is not None:
        samples = [s for s in samples if s >= since]
    if until is not None:
        samples = [s for s in samples if s <= until]
    edges = ([since] if since is not None else []) + samples + ([until] if until is not None else [])
    gaps = [{'from': a, 'to': b, 'seconds': round(b - a, 3)} for a, b in zip(edges, edges[1:]) if b - a > max_gap]
    return len(samples), gaps


def heartbeat_script():
    """Start the heartbeat unit unless it is already active; print its state."""
    return (f'mkdir -p {DIR}; if ! systemctl is-active --quiet {UNIT}; then '
            f'systemctl reset-failed {UNIT} 2>/dev/null || true; '
            f'systemd-run --unit={UNIT} --property=Restart=always /bin/sh -c '
            + shlex.quote(f'while :; do date +%s.%N >> {DIR}/heartbeat; sleep 1; done')
            + f'; sleep 3; fi; systemctl is-active {UNIT}')


def write_script(token):
    return (f'test ! -e {DIR}/marker; mkdir -p {DIR}; '
            f'printf "%s\\n" {shlex.quote(token)} > {DIR}/marker; sync {DIR}/marker; '
            f'cd {DIR} && sha256sum marker > marker.sha256 && sync marker.sha256; '
            + heartbeat_script() + f'; cat {DIR}/marker.sha256')


def guest_clock(call):
    """Guest clock minus workstation clock, bracketed by the workstation time around one guest
    `date` call; uncertainty is half the round trip."""
    before = time.time()
    guest_now = float(call('date +%s.%N'))
    after = time.time()
    return {'offset_seconds': round(guest_now - (before + after) / 2, 3), 'uncertainty_seconds': round((after - before) / 2, 3)}


def check_script():
    return (f'cd {DIR} && sha256sum -c marker.sha256 >&2 && cat marker.sha256 && echo --- && '
            f'(systemctl is-active {UNIT} || true) && echo --- && cat heartbeat')


def main():
    from fresh_install_host import guest, ssh
    from fresh_install_lab import validate_lab
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--lab-dir', type=Path, required=True)
    p.add_argument('--vm-id', required=True, help='UUID of the installed gate guest')
    p.add_argument('--stage', choices=('write', 'heartbeat', 'check'), required=True)
    p.add_argument('--evidence', type=Path, required=True)
    p.add_argument('--label', default='check', help='evidence name suffix for heartbeat and check')
    p.add_argument('--max-gap', type=float, default=3.0, help='largest accepted heartbeat gap in seconds')
    p.add_argument('--since', type=float, help='window start, epoch seconds (for example an outage start)')
    p.add_argument('--until', type=float, help='window end, epoch seconds')
    p.add_argument('--allow-gap', action='store_true', help='record gaps without failing (host reboot windows)')
    args = p.parse_args()
    vm_id = str(uuid.UUID(args.vm_id))
    lab = validate_lab(args.lab_dir)
    assert ssh(lab, 'hostname', text=True).stdout.strip() == json.loads((lab / 'config.json').read_text())['hostname']
    assert vm_id in ssh(lab, 'virsh -c qemu:///system list --uuid', text=True).stdout.split(), 'guest must be running'
    args.evidence.mkdir(parents=True, exist_ok=True)
    record = args.evidence / 'guest-marker.json'
    run = lambda script: guest(lab, vm_id, script)
    if args.stage == 'write':
        assert not record.exists(), 'marker already recorded in this evidence directory'
        token = 'nex-phase2-gate ' + secrets.token_hex(16)
        out = run(write_script(token)).split()
        digest = hashlib.sha256((token + '\n').encode()).hexdigest()
        assert out[0] == 'active' and out[1] == digest, out
        record.write_text(json.dumps({'vm_id': vm_id, 'marker_sha256': digest, 'written_at': time.time(), 'guest_clock': guest_clock(run),
            'heartbeat_unit': UNIT, 'method': 'qemu-guest-agent guest-exec; transient systemd unit writes epoch time every 1 s'}, indent=2) + '\n')
        print('PASS: marker written with SHA-256 ' + digest + '; heartbeat unit active')
        return
    expected = json.loads(record.read_text())
    assert expected['vm_id'] == vm_id
    if args.stage == 'heartbeat':
        output = args.evidence / f'guest-heartbeat-{args.label}.json'
        assert not output.exists(), 'refusing to overwrite existing evidence ' + output.name
        active = run(heartbeat_script()).split()[-1]
        output.write_text(json.dumps({'vm_id': vm_id, 'started_at': time.time(), 'heartbeat_unit': active, 'guest_clock': guest_clock(run)}, indent=2) + '\n')
        assert active == 'active', active
        print('PASS: heartbeat unit active; marker kept')
        return
    output = args.evidence / f'guest-marker-{args.label}.json'
    assert not output.exists(), 'refusing to overwrite existing evidence ' + output.name
    clock = guest_clock(run)
    shift = lambda t: None if t is None else t + clock['offset_seconds']
    digest_line, active, heartbeat = run(check_script()).split('---\n')
    samples, gaps = heartbeat_gaps(heartbeat, args.max_gap, shift(args.since), shift(args.until))
    result = {'vm_id': vm_id, 'checked_at': time.time(), 'marker_sha256': digest_line.split()[0],
        'marker_unchanged': digest_line.split()[0] == expected['marker_sha256'], 'heartbeat_unit': active.strip(),
        'guest_clock': clock, 'window': {'since': args.since, 'until': args.until},
        'guest_clock_window': {'since': shift(args.since), 'until': shift(args.until)}, 'max_gap_seconds': args.max_gap,
        'samples_in_window': samples, 'gaps': gaps}
    output.write_text(json.dumps(result, indent=2) + '\n')
    assert result['marker_unchanged'], result
    assert args.until is not None or result['heartbeat_unit'] == 'active', 'heartbeat unit is not active; run the heartbeat stage after a reboot'
    assert samples, 'no heartbeat samples in the window'
    assert args.allow_gap or not gaps, gaps
    print(f'PASS: marker hash unchanged; {samples} heartbeat samples; gaps over {args.max_gap}s: {len(gaps)}')


if __name__ == '__main__':
    main()
