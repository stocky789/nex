#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""Bounded fio and iperf3 measurements inside the disposable native VM lab."""
import json
import re


def measure(guest):
    filename = '/root/nex-vm-defaults-fio.bin'
    common = ['fio', '--name=nex-defaults', f'--filename={filename}', '--size=256M',
              '--direct=1', '--ioengine=libaio', '--group_reporting=1', '--output-format=json']
    guest(*(common + ['--rw=write', '--bs=1M', '--iodepth=1']), timeout=90)
    disk, network = {}, {}
    for trial in range(3):
        for rw in ('randread', 'randwrite'):
            raw = json.loads(guest(*(common + [f'--rw={rw}', '--bs=4k', '--iodepth=32',
                                                '--time_based=1', '--runtime=3']), timeout=90))
            job = raw['jobs'][0]
            io = job['read' if rw == 'randread' else 'write']
            disk.setdefault(f'{rw}-depth32', []).append({'bytes_per_second': io['bw_bytes'], 'iops': io['iops'],
                                                        'p99_latency_us': io['clat_ns']['percentile']['99.000000'] / 1000,
                                                        'guest_user_cpu_percent': job['usr_cpu'], 'guest_system_cpu_percent': job['sys_cpu']})
        for reverse in (False, True):
            for streams in (1, 4):
                args = ['iperf3', '-c', '198.18.99.1', '-P', str(streams), '-t', '3', '--json']
                if reverse:
                    args.append('-R')
                raw = json.loads(guest(*args, timeout=45))
                assert not raw.get('error'), raw.get('error')
                end = raw['end']
                pings = guest('ping', '-n', '-c', '20', '-i', '0.05', '198.18.99.1', timeout=15)
                latencies = sorted(float(value) for value in re.findall(r'time=([0-9.]+) ms', pings))
                assert len(latencies) == 20, pings
                key = ('host-to-guest' if reverse else 'guest-to-host') + f'-streams{streams}'
                network.setdefault(key, []).append({'bits_per_second': end['sum_received']['bits_per_second'],
                                                     'guest_cpu_percent': end['cpu_utilization_percent']['host_total'],
                                                     'ping_p99_ms': latencies[19],
                                                     'retransmits': end['sum_sent'].get('retransmits', 0)})
    guest('rm', '-f', filename)
    return {'fio': disk, 'iperf3': network}
