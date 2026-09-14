# Gallery virtual-machine captures

Recapture of the website gallery from a local Controller, adding managed-VM views.

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

Same harness and host fixture as the [9 September captures](../website/checks.md), plus: pool
storage and guest network registration, 64 KiB ISO upload, and three VMs created through
`/api/v1/vms`. A continuous simulated agent answers commands so operations complete and VMs
report running. No libvirt, guest, host networking or image editing; UI evidence only.

```sh
.cache/phase1-qa-venv/bin/python tests/integration/website_controller_captures.py \
  --output website/static/gallery \
  --evidence docs/qa/website-controller-captures/results.json
```

## Results

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

Image, Controller binary and `web/dist` hashes: [website-controller-captures/results.json](../website-controller-captures/results.json).

Local build reviewed with `tests/integration/website_browser.py`: all 12 groups passed, including
gallery navigation/alt/keyboard/Home at 1440/768/360px and JS-disabled
([website-browser-results.json](website-browser-results.json)). Rendered `gallery-1440.png`
inspected (git-ignored, not in the repository).

## Limits

- Inventory, disks, NICs and VMs are a declared fixture; `running` is simulated, no guest booted.
- Not deployed. [Deployed hashes](../website-security/deployed-hashes.json) still describe the
  previous release and need refreshing after the next publish.
