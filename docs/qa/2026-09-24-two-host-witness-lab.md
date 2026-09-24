# Two-host qdevice and watchdog partition check

Date: 2026-09-24. This is a **disposable nested-lab observation** for the native
Corosync qdevice/qnetd path described in
[the two-host pool fencing plan](../plans/2026-09-22-two-host-pool-fencing.md).
It does not establish Nex Core witness integration, Controller registration, promotion
authorization, or acceptance of the planned feature.

## Environment and method

The existing `cluster_ha_lab.py` fixture was validated with its `check_lab` guard and
`ready.json`. Only two Debian 13 guests were Corosync members. A third disposable guest
ran qnetd outside that cluster. The fourth fixture guest remained idle. No live host,
external pool, or other QEMU guest was contacted. The guest management SSH links used
the fixture's NAT interfaces; all Corosync membership traffic used the private ring.

Corosync, Pacemaker, `corosync-qdevice`, `corosync-qnetd`, and `sbd` were installed only
inside the disposable guests. The two-member Corosync configuration used a qdevice with
one vote, model `net`, algorithm `ffsplit`, TLS enabled, and the lowest-node-ID tie-break.
`expected_votes` was three and `wait_for_all` was enabled. Both members used the same
pool-style cluster name and the qnetd guest's private-ring address.

The qnetd package had already initialized its NSS database and CA during installation;
an explicit second `corosync-qnetd-certutil -i` therefore refused with “database already
exists.” The qnetd status reported TLS supported and client certificates required. The
native client enrollment sequence was:

1. Export qnetd's public CA certificate and initialize the qdevice NSS database with
   `corosync-qdevice-net-certutil -i -c <qnetd-ca-certificate>`.
2. Generate a CSR with
   `corosync-qdevice-net-certutil -r -n <cluster-name>` on one member.
3. Sign it on qnetd with
   `corosync-qnetd-certutil -s -c <request> -n <cluster-name>`.
4. Import the signed certificate and private key on the originating member with
   `corosync-qdevice-net-certutil -M -c <signed-certificate> -n <cluster-name>`.
5. Transfer that generated cluster PKCS#12 bundle to the second member and import it
   with `corosync-qdevice-net-certutil -m -c <cluster-bundle>`.

Importing the signed public certificate directly on the second member failed because
that guest did not hold the matching private key. Importing the bundle from the first
member succeeded. Thus this native flow uses a cluster client private key shared by
the qdevice members. The Controller/qnetd CA registration path and secure replacement
of that shared credential were not exercised.

Both QEMU guests initially lacked a watchdog device. On these owned guests only, QMP
`device_add` accepted an `i6300esb` device, and each guest then exposed an emulated
`/dev/watchdog`. QEMU's watchdog action default was confirmed as `reset`. Each guest ran
diskless SBD with `SBD_PACEMAKER=yes`, `SBD_STARTMODE=always`, and
`SBD_DELAY_START=yes`.

## Startup constraints observed

The first SBD attempt used a five-second watchdog timeout. SBD rejected qdevice quorum
as untrusted because the native qdevice `sync_timeout` default was 30 seconds, greater
than the watchdog timeout. SBD logged:

> Connected to corosync but quorum using qdevice is distrusted for SBD as qdevice-sync_timeout (30s) > watchdog-timeout (5s).

The service did not become active. The native SBD manual says `SBD_DELAY_START=yes`
delays startup and that `TimeoutStartSec` may need to be increased. With delay retained,
a 40-second SBD watchdog timeout, and a guest-only `sbd.service` drop-in setting
`TimeoutStartSec=180s`, both SBD services became active after about 80 seconds. The
40-second device timeout exceeded qdevice's 30-second sync timeout. The sysfs watchdog
timeout read as 40, SBD owned the device, and the prior qdevice-distrust warning was
absent on the successful start. The service and timeout adjustment were temporary lab
configuration, not a repository change.

Pacemaker was active on both members with `stonith-enabled=true` and the shared CIB
property `no-quorum-policy=suicide`. There were no managed resources. Before partition,
both guests reported all four services active, three expected and three total votes,
`Quorate: Yes`, a qdevice vote, and qdevice state `Connected`. qnetd reported TLS with
client certificates required, two connected clients, and one connected cluster. Both
boot IDs were unchanged from cluster startup.

## Peer-only partition result

The test inserted symmetric iptables input/output drops for each peer's address on
`ens4` only. In this report those fixture-only addresses are replaced with placeholders;
the rules did not block `ens3` NAT SSH or traffic to the qnetd guest. The effective rule
shape was:

```sh
iptables -I INPUT 1 -i ens4 -s <peer-ring-address> -j DROP
iptables -I OUTPUT 1 -o ens4 -d <peer-ring-address> -j DROP
```

With the two members separated, qnetd remained reachable from both sides. The `ffsplit`
low-node-ID tie-break gave the vote to host A:

| Observation | Host A | Host B |
|---|---|---|
| Corosync membership | one node | one node |
| qdevice TLS state | Connected | Connected |
| qdevice vote | one | zero |
| Corosync total / expected votes | 2 / 3 | 1 / 3 |
| Quorate | Yes | No |
| Boot ID after the event | unchanged | changed after reboot |

At 07:00:57 UTC, host B's prior-boot journal recorded quorum loss, Pacemaker's
`no-quorum-policy=suicide` response, an SBD panic request, Pacemaker escalation, and
`watchdog0: watchdog did not stop!`. A subsequent check at 07:01:46 UTC found a new boot
ID on B, while A's boot ID was unchanged and A remained quorate with the qdevice vote.
The previous-boot journal ends during Pacemaker's panic path; it does not independently
establish whether Pacemaker's direct panic or the still-armed emulated watchdog caused
the reset. The observed result is that native quorum loss under Pacemaker's suicide
policy was followed by a host reboot; it is not an isolated watchdog-expiry test.

The peer rules were removed from host A immediately after the reboot was observed.
Host B rejoined; both nodes again reported qdevice connected, quorum, and active Corosync,
qdevice, SBD, and Pacemaker services. Only B's boot ID changed. No second reset was
observed. The qnetd guest remained active throughout.

### Sanitized command evidence

The following commands were run over NAT SSH before fixture cleanup:

```sh
systemctl is-active corosync corosync-qdevice sbd pacemaker
corosync-quorumtool -s
corosync-qdevice-tool -s
corosync-qnetd-tool -s
cat /sys/class/watchdog/watchdog0/timeout
cat /proc/sys/kernel/random/boot_id
journalctl -b -1 -o short-unix --no-pager -n 20
```

This is a short **transcribed, sanitized excerpt** of the command output, not an untouched
capture. Host names, ring addresses, boot IDs and process IDs are replaced with labels;
raw fixture logs were removed during cleanup.

Before partition, both members reported:

```text
Quorate:          Yes
Expected votes:   3
Total votes:      3
Flags:            Quorate WaitForAll Qdevice
State:            Connected
```

After partition, the winner reported one member, two total votes of three, and `Quorate: Yes`;
the losing member remained qdevice-connected but reported:

```text
Nodes:            1
Quorate:          No
Expected votes:   3
Total votes:      1
Quorum:           2 Activity blocked
         2          1   A,NV,NMW host-b (local)
         0          0            Qdevice (votes 1)
State:            Connected
```

Its previous-boot journal ended with:

```text
07:00:57Z pacemaker-controld: warning: Quorum lost
07:00:57Z pacemaker-controld: emerg: Signaling sbd to panic the system: Quorum lost
07:00:57Z pacemaker-controld: emerg: Panicking directly because couldn't signal sbd
07:00:57Z systemd: sbd.service: Main process exited, code=killed, status=9/KILL
07:00:57Z kernel: watchdog: watchdog0: watchdog did not stop!
```

The next status query showed the losing guest's boot ID had changed and the winner's had not.
Because this excerpt is sanitized and transcribed, the time and lines should be read as the
recorded guest journal observations, not as a raw machine log attachment.

## Limits and follow-up

- This tested Debian's native Corosync/qdevice/qnetd and Pacemaker/SBD behavior in QEMU.
  Nex Core's agent was not run, and no membership record, witness registration API,
  Core status endpoint, exclusion proof, or promotion path was involved. qnetd remained reachable;
  witness loss and its effect on the surviving host were not tested.
- The qnetd guest supplied its own NSS CA. The Core-to-Controller registration and
  durable CA-pin/replacement contract were not implemented at the time of this lab and were not
  exercised here. Later implementation and focused checks are recorded separately in the
  [Controller registration QA](2026-09-24-controller-witness-registration.md) and
  [Core witness change record](../changes/2026-09-24-core-witness-registration.json).
- The shared qdevice PKCS#12 private key was transferred manually in this lab. Core-owned sealed
  distribution and replacement were implemented later, but were not exercised in this lab.
- The 40-second SBD timeout and 180-second systemd start limit are measurements for
  this nested environment. They are not hardware timings or production defaults.
- The test did not isolate watchdog expiry from Pacemaker's panic. Physical watchdogs,
  storage workloads, real network switches, and any claims of safe automatic promotion
  remain untested.

The controlled fixture was cleaned up after this record was written; its guests, overlays,
qnetd test CA, temporary certificates, and local test artifacts were removed.
