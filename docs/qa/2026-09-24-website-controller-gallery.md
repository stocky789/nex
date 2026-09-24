# Controller gallery refresh

Date: 2026-09-24. Scope: seven Nex Controller screenshots and their website captions. The ten Nex Core installer images were not changed.

## Method and results

- Built the current Controller UI with `npm --prefix web run build`. Ran `tests/integration/website_controller_captures.py` with Python Playwright and Chromium 151.0.7922.34 at 1440×1000. Its disposable loopback Controller used temporary state, TLS enrollment, mTLS reports, and a protocol-level simulated host. The host fixture acknowledged registration, media import and VM creation; the screenshots came from the real browser UI. [Capture results and image hashes](website-controller-captures/results.json) record all five passing checks.
- Visually inspected all seven final gallery PNGs: populated Overview, VMs, VM slide-over, Create VM review, Networks, Storage and the top-bar activity menu. Captions and alt text describe what each image shows. No credentials, private addresses, personal labels or generated host IDs appear in the captures; the script also rejected a deliberately private label after saving the safe images.
- Ran `npm --prefix website run check` (zero errors and warnings) and `npm --prefix website run build` (static output generated). Served that output on loopback and ran `tests/integration/website_browser.py` at 1440, 768 and 360 px. All 13 reported groups passed, including gallery image loading, alt text, keyboard navigation and JavaScript-disabled access. Inspected the desktop and mobile gallery captures locally; no clipped content or broken images was visible. The disposable browser evidence is under `.cache/website-gallery-browser/` and is not tracked.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'` passed (one test); `python3 scripts/check_docs.py --worktree` passed for 12 changed paths, and `git diff --check` found no whitespace errors.
- Independent review found generated host and operation IDs in the first Events capture. Replaced it with the current top-bar task menu, added a host-ID absence assertion, regenerated the images, and reran the capture and website browser checks. The reviewer inspected the corrected image and found no remaining issue.

## Limits

The host and its three VMs are fictional demo data. These captures verify the Controller rendering and API flow against a simulated Core protocol peer; they do not verify libvirt, storage, guest execution or recovery. The public gallery at `https://nexvirtualization.com/gallery/` returned HTTP 200 on 24 September 2026, but all seven served Controller image hashes and its Activity caption still matched the old version. The private address supplied for this request refused direct HTTP and HTTPS connections from this workspace. The updated static files were reviewed locally and were not published. No live host was changed.
