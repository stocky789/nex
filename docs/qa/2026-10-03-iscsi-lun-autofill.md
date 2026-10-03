# iSCSI LUN detection and autofill verification

Verification window: 2026-10-03–2026-10-04. Scope: the development worktree's selected-target inspection and ordinary/clustered storage forms, not release or phase acceptance.

## Contract and state

API impact: **changed contract**. Existing `POST /api/v1/storage/discover` accepts an optional iSCSI `target_iqn`; authenticated Core advertises `storage-iscsi-inspect-v1` and returns transient `iscsi_luns`. Controller validates administrator/CSRF/host scope and relays the request; Core performs inspection. Target listing and registration remain separate operations.

Guide impact: **updated** in [storage repositories](../guide/storage-repositories.md). State owner: **none**. Inspection creates no durable pool or Controller state. Existing registration identity, credentials, access validation and exclusive-initiator attestation remain Core's responsibility.

Browser cancellation invalidates the relay and late replies. Already-dispatched Core work continues within its independent deadline; cancelling Core's own context kills its helper and closes that helper's connection.

## Environment and fingerprints

- Native lab: disposable, unprivileged Debian 13 container; userspace `tgt` targets backed by newly created regular files, with every exposed data LUN read-only. No workstation block devices, real hosts or NAS were used.
- Libraries: `libiscsi7 1.20.0-4`, `libblkid1 2.41.5-0+deb13u1`; target engine `tgt 1:1.0.85-1.3`.
- Core smoke: actual `vmhost.DiscoverStorage` and capability probe, compiled with Go 1.27.1; actual packaged-path native helper built with Debian GCC. This was not a simulated filesystem parser.
- UI: built React/TypeScript application in headless Chromium, with explicitly simulated Controller/Core API replies. Node 24.21.0. Browser fixtures establish UI behavior, not real host authorization or storage correctness.
- Native helper SHA-256: `e07bebb9a2f33a431e289e1cf95a542455bb24dd80e96e1d309faf1897bbf5b0`.
- Native source SHA-256: `cae28ab87ff2b73bffab3071baa2079ae8cbd9791578e7826b5f2496f344fc7b`.
- Built `web/dist/app.js` SHA-256: `a2734e173f80d57abf3bb763661c7bc8f58e00df1c6078dcdbb83ca1def02b62`.

## Actual native walkthrough

A throwaway runner submitted typed requests to the real Core implementation. Each selected-target request opened a private userspace iSCSI connection using the lab Core's initiator IQN. The observed results were:

| Case | Observed result |
| --- | --- |
| Five data LUNs on one target | Reported LUNs 1–5, each with its own full NAA6 WWID and real capacity; the target's controller-only LUN was excluded. |
| Whole-LUN ext4 and XFS | Reported each pre-created filesystem's exact UUID and type. |
| Raw unformatted file | Reported capacity and WWID, no filesystem UUID, and a missing-signature reason. |
| GPT with one ext4 partition | Reported that partition's exact existing UUID and type, not a guessed whole-device UUID. |
| GPT with two ext4 filesystems | Left UUID blank and reported multiple/ambiguous signatures. |
| Whole-LUN GFS2 | Reported the exact existing GFS2 UUID and type from a `lock_nolock` fixture; this does not establish DLM readiness, and no clustered mount or apply was attempted. |
| Correct CHAP | Authenticated and returned the single data LUN's existing identity. |
| Incorrect CHAP | Returned a sanitized failed observation without credential values. |
| Target with only a controller LUN | Returned successful empty block-LUN observation. |

SHA-256 hashes of **all** backing images matched before and after inspection. The native node/discovery directories and kernel iSCSI-session inventory also matched. No kernel login, persistent node creation, mount, format, write, or existing-session logout was performed by inspection. CHAP values were absent from returned JSON.

A second native smoke held an existing userspace session open with automatic reconnection disabled, ran six concurrent Core inspections of the same target, then read capacity again through the original session. All six observations succeeded and the original session still returned the same capacity without reconnecting.

An unresponsive loopback peer accepted the helper's actual login traffic but sent no response:

- Core context cancellation returned `cancelled` and the peer observed socket closure in **0.159 s**.
- Without early cancellation, the native command timeout returned a sanitized failed observation and closed the socket in **2.010 s**, inside Core's eight-second bound.
- Backing hashes and native/kernel iSCSI records remained unchanged after these cases too.

These are lab measurements, not production latency guarantees. Throwaway runners and fixture setup are removed after verification; the permanent checks below cover the contract and identified regression boundaries.

## Browser walkthrough and visual inspection

Both existing browser harnesses passed against the built UI:

```sh
uv run --no-project --with playwright python tests/integration/storage_repositories_browser.py --evidence .cache/storage-browser-iscsi-autofill-final
uv run --no-project --with playwright python tests/integration/cluster_storage_browser.py
```

The ordinary form exercised actual LUN zero autofill, explicit keyboard selection from multiple LUNs, blank/unsupported identity notices, empty/error responses, transient CHAP request bodies, host/portal/IQN/CHAP changes, cancellation, manual edits, close/reopen, late replies and older-Core manual fallback. Failed registration and failed credential handoff preserved manual identities and exclusivity without restarting inspection; the latter cleared only the secret. An existing iSCSI name-only update submitted its saved source unchanged without inspection. Ambiguous filesystem layout was exercised natively, not as an ordinary browser response.

The clustered form exercised single/multiple, blank, unsupported and failed observations, invalidated stale source replies, and explicitly retried the same target after cancelled or failed inspection. Its final full run used native Space/End/Enter events to select LUN 4, and the `cluster-storage-iscsi-multiple-luns.png` capture was visually inspected: the focused picker and exact identity fields fit the existing form without overflow. Cancelled/reopened forms did not silently restart inspection. Its harness did not exercise empty or ambiguous inspection replies. Existing cluster validation, fencing and Core-owned operation reporting remained unchanged.

The `iscsi-single-lun.png` and `iscsi-multiple-luns.png` captures from the ordinary harness were visually inspected. Labels, detected values, focus and the manual exclusivity checkbox were visible without horizontal overflow. A separate managed-browser walkthrough inspected the final dark surface and used native Space/End/Enter events to select LUN 7; its exact WWID and UUID appeared while exclusivity remained unchecked.

Network audit of that separate simulated walkthrough: exactly two discovery POSTs—one target listing and one selected-target inspection—both HTTP 200, with CSRF headers, no credentials in URLs, no duplicate inspection and no console errors. Each fixture response took 1 ms; this says nothing about production network performance. Periodic inventory refreshes were expected and separate from inspection.

## Runnable checks and independent review

Executed successfully:

- Before the review-fix round: `go test ./internal/protocol ./internal/vmhost ./internal/agent ./internal/controller`; after the final selected-target-only credential correction, protocol and Controller suites passed again.
- `npm --prefix web run typecheck`, `npm --prefix web test`, `npm --prefix web run build`; the focused storage unit test passed again after removing incidental wording assertions.
- Debian compilation of the real native helper and `nex-iscsi-inspect-test.c` with `-Wall -Wextra -Werror -O2 -Wl,--export-dynamic`; native regressions and `--probe` passed after the final corrections.
- The pinned Debian compile/run command added to both forge phase-one workflows was executed locally and passed. The package builder also runs this native regression before assembling its output.
- `PYTHONPATH=tests python3 -m unittest test_ci_security test_release_version test_ci_serialization`: **55 tests passed**.
- Existing package, image-composition and source-build checks: **21**, **30** and **9** tests passed respectively.
- `python3 scripts/generate_api.py` and `python3 scripts/generate_api.py --check`: generated reference current.
- `python3 scripts/check_docs.py --worktree`: documentation structure and supplied assessments passed for 43 uncommitted paths; this does not replace the independent semantic reviews.

Permanent regressions cover nonpersistent SendTargets exit 21, actual LUN zero JSON, flat-addressed native/API LUN 16640 and its registration/HCTL consumer, bounded first-session UNIT ATTENTION, unrelated/persistent SCSI failures, NAA preference and conflicts, EUI-only unsupported WWID, native read-adapter bounds, selected-target CHAP 255/256-byte limits, unaffected target-only/SMB limits, Core scope/capability gates, cancellation and browser draft preservation.

Independent reviewers checked Core/native/API/packaging safety and UI lifecycle behavior. Their reported addressing, credential truncation, first-session readiness, WWID preference/compatibility, CI coverage and draft-generation defects were corrected; both final source reviews reported no remaining must-fix finding. Reviewers did not execute checks or operate targets; runtime evidence above was obtained by the parent.

## Limits

- Playwright and Chromium versions: not recorded for the harness runs above (cached browsers exist locally; the Python Playwright package is not installed outside the `uv` run).
- The review-fix round (purpose capability, result validation, 64-LUN refusal, explicit-trigger inspection, cached capability probe, shorter helper alarm) changed code after these measurements; native smokes and browser harnesses were not rerun for it, and the earlier smokes were one-off, non-reproducible runs with removed throwaway runners.
- The browser harnesses were updated for that round but not executed in this session.

- No production host/NAS operation, real-disk mutation, installed-host upgrade, full package/image build, Secure Boot or hardware validation was performed. This does not close existing release/image/phase gates.
- Native target listing could not be exercised in this unprivileged container because its kernel iSCSI transport and daemon were unavailable. No workstation module was loaded to work around it; the existing command regression covers empty SendTargets semantics.
- `tgt` reserves LUN zero for its control device. Data-LUN-zero behavior was exercised in protocol regressions and browser fixtures; flat addressing was exercised in native parser and Go registration/HCTL regressions. Neither used a live data-LUN-zero or flat-addressed target.
- EUI-only identities do not provide the native WWN required by this registration profile; inspection leaves WWID missing with a reason. A successful observation is not evidence of exclusive access, cluster fencing/quorum, writable admission or a usable restore.
- The full-worktree repository-privacy check failed on pre-existing unrelated root core dumps containing personal paths. Those files were left untouched; task-authored public text and fictional fixtures were manually reviewed. No privacy-gate pass or cleanup of existing artifacts/history is claimed.
- An exact task-only net line count against the incoming dirty worktree is unavailable because the initial overlapping file baseline was not retained; the earlier worker count is not represented as a final count. Existing unrelated edits were preserved.
