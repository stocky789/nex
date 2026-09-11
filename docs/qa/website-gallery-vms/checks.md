# Gallery virtual-machine captures

Recapture of the website gallery from a locally deployed Nex Controller, extended with
the managed virtual-machine views so the gallery shows the VM work added since the
9 September 2026 captures.

## Environment

| Item | Value |
| --- | --- |
| Date | 11 September 2026 |
| Controller | Built from this working tree by the capture harness (`go build ./cmd/nex-controller`) |
| Controller UI | `web/dist` rebuilt from `web/src`; `npm run typecheck` clean |
| Browser | Chromium 151.0.7922.34 (Playwright 1.62.0), viewport 1440x1000, UTC, en-GB |
| Host inventory | Explicitly simulated demo fixture reported over enrolled agent mTLS |
| Website | SvelteKit build, `npm run check` 0 errors 0 warnings |

## Method

`tests/integration/website_controller_captures.py` builds and starts the real Controller
on a loopback TLS listener, creates a site, issues an enrollment token, enrolls a
simulated host by CSR, and reports a fictional inventory over agent mTLS. The four host
views are captured first, against the same clean fixture as the previous run.

The harness then registers pool storage and a guest network, uploads a 64 KiB ISO
fixture, and creates three VMs through the real `/api/v1/vms` contract. A continuous
simulated agent answers the Controller's commands and reports the resulting definitions,
so every operation reaches a recorded result and the VMs are observed running. The three
managed-VM views are captured from that state.

No libvirt, no guest execution, no host networking changes and no image editing are
involved. Nex Core was not installed on the workstation; the host side is a protocol-level
fixture, which is why these captures establish UI behaviour only, not hypervisor behaviour.

Command:

```sh
.cache/phase1-qa-venv/bin/python tests/integration/website_controller_captures.py \
  --output website/static/gallery \
  --evidence docs/qa/website-controller-captures/results.json
```

## Results

All five harness checks passed:

| Check | Result |
| --- | --- |
| Real Controller, neutral site, CSR enrollment creating the automatic pool with the host as master | Pass |
| Three demo VMs created through the real API on registered storage, network and uploaded media | Pass |
| Pool VM list, VM details and the completed six-step Create VM review captured | Pass |
| Privacy guard rejected the original workstation label rendered by the real Controller | Pass |
| Four real browser resource views, neutral rendered identity, no credential fields or browser errors | Pass |

Captured images, all inspected individually:

| Image | View |
| --- | --- |
| `desktop-shell.png` | Host General: overview and health checks |
| `vms-desktop.png` | Pool Virtual machines: three VMs observed running, creation tasks succeeded |
| `vm-general-desktop.png` | VM details for `web-01` with toolbar power controls enabled |
| `create-vm-desktop.png` | Create VM step 6 of 6, Review, including the Lifecycle options |
| `networking-desktop.png` | Host Networking |
| `storage-desktop.png` | Host Storage |
| `activity-desktop.png` | Host Activity |

Per-image SHA-256 values, the Controller binary hash and the `web/dist` asset hashes are
recorded in [website-controller-captures/results.json](../website-controller-captures/results.json).

The built website was served on a local listener and reviewed with
`tests/integration/website_browser.py`. All twelve groups passed, including
`gallery navigation, images, alt text, keyboard full-size/skip and Home return` at 1440px,
768px and 360px, and `gallery navigation and screenshots available` with JavaScript
disabled. Results: [website-browser-results.json](website-browser-results.json).
The rendered page was inspected: [gallery-1440.png](gallery-1440.png).

## Limits

- The host inventory, its disks, NICs and all three VMs are a declared demo fixture. These
  captures are evidence of Controller UI rendering, not of libvirt, storage or guest behaviour.
- VM state is reported as `running` by the simulated agent; no guest was booted.
- Not deployed. These images and the rebuilt site were not published to the website
  server, so no deployed-site evidence follows from this run. The recorded
  [deployed hashes](../website-security/deployed-hashes.json) still describe the previous
  release and must be refreshed after the next publish.
