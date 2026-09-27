# Two-host witness registration investigation

Date: 2026-09-25. The observed test pool ran Nex Core 0.4.12. Its Controller
reported `forming` with `qdevice status is unavailable`; the master's witness
selection existed but `credentials_ready` was false. Both Core hosts' fresh
reports omitted `pool-witness-v1`. Host names, addresses, and identifiers are
omitted from this record.

## Reproduction and fix

Controller requires `pool-witness-v1` on the current master's report before it
signs a witness CSR. Core's report omitted that capability, so registration was
refused. `TestReportAdvertisesWitnessRegistration` failed before the fix with
`Core omitted the witness capability required by Controller registration` and
passed after adding it to Core's report.

In a disposable Debian 13 container with `corosync-qnetd`, NSS tools and
OpenSSL, a probe used current Core functions to generate a CSR, native qnetd to
sign it, and Core to import the resulting certificate and sealed member bundle.
Native `certutil -R -a` produced a text preamble and a
`NEW CERTIFICATE REQUEST` PEM block. Controller and Core accepted only
`CERTIFICATE REQUEST`; before the fix, Core import returned
`qdevice request or signed certificate is invalid`. The focused
`TestPoolWitnessNativeCredentialLifecycle` reproduced the native CSR shape and
failed before the fix. Core now returns the canonical PEM block on creation and
retry from an existing staged CSR.

With a fresh qnetd CA and fresh master/member credential directories, the
unmodified probe using the patched Core code generated the CSR, received a
native qnetd signature, imported a 3351-byte bundle on the master, and reported
`PoolWitnessCredentialsReady=true` for both master and member. No certificate
or private key was added to the repository. The generated and staged-retry CSR
also passed Go signature validation, had the exact deterministic common name,
and contained no extensions, matching Controller's input checks. A separate
native check found that
qnetd's issued certificate passes both NSS client and server usage validation;
the proposed usage-flag change was discarded.

The complete isolated lab exposed three further Core defects after registration:

- The member held a current signed replica with the witness and peer addresses, but its
  cluster reconciler read stale local membership instead. The regression now checks the
  member's verified replica and retains local maintenance and arming state.
- Both hosts started Corosync before installing one shared 128-byte authentication key.
  The master now seals its durable key into each signed replica; a member stores it before
  acknowledgement, and each host installs it before starting its pool cluster. The master
  requires an explicit key receipt so an older member cannot silently ignore the field.
- On Debian 13, `corosync-qdevice-tool -s` omits TLS and vote fields. Core now requests
  `-s -v` and refuses to mark a mismatched witness identity or CA as observed.

Focused checks passed:

```text
go test ./internal/agent -run '^TestReportAdvertisesWitnessRegistration$|^TestPoolWitness' -count=1
go test ./internal/vmhost -run '^TestPoolWitness' -count=1
go test ./internal/controller -run '^TestWitnessEndpointsAuthenticateMasterAndCheckGeneration$' -count=1
go build ./cmd/nex-core-agent ./cmd/nex-controller
```

After the full lab fixes, focused agent tests for pool cluster, authority replica and
four-host teardown, focused vmhost tests for verbose witness status and Corosync
configuration ownership, `go test ./internal/protocol`, both Go builds, API generation
check, the 74-case API contract suite, repository privacy test and the worktree documentation gate all passed. The
broader agent suite was not green because an unrelated dirty autostart test failed;
no full-suite or forge pass is claimed.

## Two-host Core and Controller lab

Two disposable Nex Core 0.4.12 guests were installed from the Nex ISO on an isolated
private virtual network. The first host deployed its bundled Controller appliance; the
second joined the first host's pool. Current source Core-local binaries were installed
on both guests. The initial run reproduced `forming`. Native checks identified a missing
Corosync key, stale member membership, and a short qdevice status response. Temporary
manual lab changes were used only to isolate each cause.

For the final retest, both hosts were stopped, the temporary native keys removed, the
member's pre-bypass local membership restored, and its key and signed replica removed.
Patched Core then delivered a new verified replica and installed matching private keys
on both hosts without manual changes. Both Corosync nodes and qdevices started; each
reported two nodes, three expected votes, three present votes, quorum, connected qnetd,
active TLS, and a cast qdevice vote. The Controller reported a **degraded** cluster with
the connected witness and zero of two watchdogs armed. That is the currently intended
safe status, not a complete automatic fencing result. Evidence is retained only in the
private disposable lab directory; no raw host identities, addresses, credentials, or
certificates are published here.

After the final key-receipt, ownership and status corrections, the latest Core binary was
restarted on both guests without reintroducing any manual bypass. Both still reported
matching private 128-byte keys, two Corosync nodes, 3/3 votes, active TLS and a cast
qdevice vote; the Controller still reported the member replica current and cluster
degraded with 0/2 watchdogs armed. The Controller appliance, both guests and lab network
processes were shut down after inspection.

The lab used an emulated watchdog and no BMC. It did not establish physical reset,
safe behavior when a witness is lost with one host already down, automatic witness
exclusion proof, or VM recovery. The existing two-host design still withholds SBD
arming and witness exclusion proof pending those safety gates in the
[two-host fencing plan](../plans/2026-09-22-two-host-pool-fencing.md).

## Authorized original test pool update

With the project owner's authorization, the isolated patched Core-local binary was
installed on both original test hosts, member first and then master, with a planned
`nex-core-local` restart on each. The binary reports `0.4.12+watchdog1`; the installed
package remains 0.4.12. The original package binary is retained on each host for
rollback. The signed update feed did not offer the patch, so this is an out-of-band
test hotfix and a later package upgrade may replace it.

After both restarts, the Controller reported fresh hosts and an authoritative two-host
pool with a current, verified member replica. Its cluster changed from **forming** to
**degraded**: the qdevice witness was connected and voting, with 0/2 watchdogs armed.
Native checks on each host showed two Corosync nodes, three expected and present votes,
quorum, a connected qdevice, active TLS and a cast witness vote. The shared Corosync key
was private and 128 bytes on both hosts. The two VMs on the master remained running.
After temporary transfer access was removed from both hosts, a final Controller read
still showed **degraded**, connected and voting, 0/2 armed, and both VMs running.

This was a service restart, not a host reboot or failover exercise. No physical watchdog
reset, witness-loss-with-one-host-down sequence, or automatic VM takeover was run. The
project owner's availability decision requires the survivor's VMs to keep running if
the witness disappears while the other host is unavailable. The witness is operating
while connected and voting regardless of its physical placement; moving it alone does
not make automatic two-host takeover safe. Private console captures and
package checks were inspected during the test and were not added to the repository.

## Separate witness and watchdog status

The follow-up Core report now carries `cluster.witness` from the pool master's native
qdevice check: `operating` requires the registered identity, pinned CA, TLS and a cast
vote; `forming` carries the native reason. The Controller passes that field through.
The UI displays **Witness · operating** separately from **Watchdog cluster · degraded**
and its 0/2 armed advisory. The former is an observation from the master, not proof
that both hosts' qdevices are healthy or that automatic takeover is safe. The status
depends on the verified vote, not where the witness runs.

The focused Core test exercised forming and operating observations while retaining
the degraded watchdog state. The UI rendering test exercised operating, forming and
an older report without the field; web typecheck and build passed. An isolated
Chromium fixture using simulated API responses exercised operating, forming, operating
witness with nonquorate cluster, absent field, stale master, membership HTTP 503 and
pending membership response at 1440×1000.
The captures were inspected: operating and degraded have distinct green and amber
labels; forming stays amber even beside an operating witness when quorum is absent;
absent and stale reports do not display a stale operating
witness; error and loading show no pool status. There were no JavaScript page errors;
the HTTP 503 produced its expected console resource error. Captures remain in an
ignored private test directory. This is browser rendering evidence, not a live native
qdevice check. This follow-up has not yet been installed on the original test pool,
so the live observations above describe the previous patched build.

## Witness loss with one host unavailable

A new isolated copy of the two-host/Core and Controller-appliance lab exercised the
previous patched Core build. Both hosts first reported two nodes, 3/3 votes, quorum,
connected TLS qdevices and a cast witness vote. SBD was inactive and Pacemaker used
`no-quorum-policy=freeze`. The second host was powered off inside the disposable
fixture; the survivor held 2/3 votes and remained quorate. The fixture then stopped
only qnetd in the Controller VM. The survivor fell to 1/3 votes and lost quorum, but
its boot ID and the running Controller VM's boot ID stayed unchanged for 138 seconds.
Core-local, Corosync, qdevice and Pacemaker remained active; SBD stayed inactive.

After qnetd restarted, the survivor's qdevice reconnected with TLS and cast a vote,
bringing the vote count to 2/3, but Corosync still reported **not quorate**, with
`Activity blocked` and `WaitForAll Qdevice` flags for more than 80 seconds while the
other host stayed off. Its rendered config had `wait_for_all: 1`. This is a native
recovery gap, not a successful requorum result. The old Controller session expired,
so no fresh Core pool-status API result was obtained during this sequence. The
follow-up Core regression now reports **Witness · operating** separately from
**Watchdog cluster · forming** whenever the verified qdevice vote has returned but
native quorum has not. It has not been deployed to the original pool.

The cloned guests, Controller VM and private network were stopped after inspection.
This tested an emulated watchdog and the running Controller appliance VM; no separate
workload VM, physical watchdog, BMC fence, automatic promotion or duplicate-writer
exclusion was exercised. Private fixture records are kept outside tracked files.

A second fresh isolated copy tested the two-host witness config without
`wait_for_all`; the three-or-more-host watchdog config retains that option. Core
rendered the candidate configuration on both guests. Reloading Corosync did not clear
its old runtime `WaitForAll` flag, so the fixture restarted Corosync while both hosts
were available and SBD was inactive. It then observed 3/3 quorate with both hosts,
2/3 quorate with one host off, 1/3 inquorate when qnetd stopped, and 2/3 quorate
about nine seconds after qnetd returned while that host stayed off. The survivor
and Controller VM kept their boot IDs; the VM remained running. A one-host Corosync
service cold start with qnetd available formed quorum; with qnetd unavailable it
stayed inquorate at 1/2 votes, then regained quorum when qnetd returned. SBD stayed
inactive and Pacemaker policy remained `freeze`. All fixture processes were stopped.

This supports the scoped render change and exposes a rollout requirement: a host
already running Corosync with `WaitForAll` needs a controlled Corosync restart after
updating its config; a reload alone does not apply this runtime change. The test did
not reboot a physical host or prove a simultaneous split, fencing, or automatic
takeover. The new renderer and status fields have not been installed on the original
test pool.

Independent review found a transition hazard: ejecting from an armed three-host
pool could leave SBD feeding while Core applied a two-host witness configuration.
The candidate records feeding intent before starting SBD, refuses an eject until
all hosts stop feeding, rechecks before the durable removal, and refuses the
two-host witness config while a membership row or the local device still feeds.
The master refuses a new feeding intent after a maintenance drain, even when the
member has not yet received the new membership. Focused regressions cover the
eject, maintenance, arming and cluster guards. A transition
already stranded with two votes can still wait for the third to return; no automatic
two-host takeover is claimed. The original two-host pool has SBD inactive; this
transition guard has simulated coverage, not a physical watchdog exercise.
