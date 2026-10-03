# Console single-use tunnel correction — 2026-10-02

## Scope and ownership

Development version 0.4.19, uncommitted worktree. BUG-1839 corrects the console
pairing failure found during the earlier [backup review verification](2026-10-02-backup-review-fixes.md).
Controller retains only a transient in-memory admission flag; no pool setting,
identity, guest work or durable record moves out of Core. Controller replacement
ends the console connection without stopping its workload, as before.

API impact: **existing contract**. `POST /api/v1/agent/console/{token}` already
promises one host tunnel per ticket and refusal of duplicate tunnels. VM and host
browser streams share this pairing. Authentication, payloads, statuses, ticket
expiry and Core compatibility are unchanged; duplicate admission is now atomic
before hijacking the HTTP connection, not inferred from channel occupancy.

Guide impact: **none**. The console guide already documents single-use tickets,
a new ticket for every retry, automatic reconnect, and unchanged running guests
when Controller disconnects. No UI or operator steps changed.

## Method and environment

Linux amd64, Go `go1.27.1-X:nodwarf5`, repository Go test tools. Disposable Controller
SQLite state and loopback TCP connections only; no production endpoint, real guest,
libvirt socket, disk or host was changed. HTTP regression handlers use the existing
identity-injection convention; their WebSocket upgrades and byte relay are real.

The deterministic regressions stop the first handshake at two distinct boundaries:

- First host tunnel admitted but its HTTP hijack has not completed.
- First host tunnel dequeued by the browser, whose WebSocket hijack has not completed.

Both attempt a second host tunnel while the first request remains blocked. Channels,
not timing sleeps, establish those interleavings. The existing relay check still
verifies host-to-browser binary RFB frames, masked browser-to-host bytes, ticket reuse
refusal, wrong host/origin, and closing the browser when the host tunnel ends.

## Executed checks

Before the implementation fix, the new deterministic checks failed:

```sh
go test -race -count=1 -timeout 2m ./internal/controller \
  -run '^TestConsole(AuthorizationPairingAndRelay|RejectsConcurrentHostTunnelBeforeUpgrade)$'
```

Observed `second tunnel accepted: 101` and
`second host tunnel admitted before the first upgrade: 101`.

After the implementation fix, the same two regressions passed ten repetitions:

```sh
go test -race -count=10 -timeout 2m ./internal/controller \
  -run '^TestConsole(AuthorizationPairingAndRelay|RejectsConcurrentHostTunnelBeforeUpgrade)$'
```

Controller reported 16.514 seconds. The original buffered-channel admission check
was replaced, not augmented with a delay or a retry workaround.

## Full-suite and runtime verification

The complete Go suite subsequently passed:

```sh
go test -race -count=1 -timeout 20m ./...
```

All 13 packages with tests passed; two additional packages reported no test files.
Controller reported 437.675 seconds, Core agent 261.490 seconds, and the command
finished in 439.68 seconds. This resolves the earlier full-suite console failure;
no test was skipped or weakened to hide duplicate admission in the new regression.

The disposable standalone program was then executed:

```sh
go run -race .cache/console-pairing-smoke.go
```

It opened the public Controller runtime with private temporary SQLite state and
actual loopback HTTPS, without injecting authentication or database/session state.
The smoke used a verified server certificate, actual administrator session and CSRF,
API site/token/CSR enrollment, and the enrolled host's client certificate. Observed:

| Check | Actual result |
| --- | --- |
| API login, enrollment and three mTLS reports | `200` |
| Site, enrollment token and host-console ticket | `201` |
| Console request delivery | Exactly once |
| Agent request without a client certificate | `401` |
| Eight simultaneous host-tunnel upgrades | One `101`, seven `404` |
| Same-origin browser upgrade with the real session cookie | `101` |
| Host-to-browser binary RFB and masked reverse relay | Exact 12 bytes each |
| Reusing the agent ticket or browser ticket | `404` each |

The program reported `status: passed` under the race detector and removed its private
temporary state. It was a throwaway smoke, not a permanent additional test; its
source was removed after execution. The RFB producer is simulated, not a real guest.

`python3 scripts/generate_api.py --check` passed for all three generated references;
the focused API contract suite passed 82 checks. Independent read-only review found
no remaining patch-introduced defect; the reviewer did not run the checks.

## Limits

- Loopback RFB bytes are simulated display traffic, not an actual VM or host terminal.
- No new browser GUI walkthrough or native guest-console certification is claimed;
  the changed surface is HTTP tunnel admission and operator steps are unchanged.
- Existing forge and GitHub Go jobs include these regression functions automatically;
  no remote CI run or publication is claimed.
