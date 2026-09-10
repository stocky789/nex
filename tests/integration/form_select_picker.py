#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""BUG-0064: Controller select pickers must render in the page and stay open.

Native <select> pickers are separate OS popup windows. On some Wayland desktops the
compositor dismisses that popup on the pointer movement that follows the click, so every
Create VM dropdown opened and closed at once. The stylesheet sets appearance: base-select,
which renders the picker in the page (Chromium 135+), where a compositor cannot close it.
This check clicks a Bus select under the Controller stylesheet in headless Chromium and
requires the picker to open, survive the re-render React applies to controlled selects,
and appear in a page capture.
"""
import argparse
from datetime import datetime, timezone
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import threading
import time

from phase1 import ROOT

SHELL = """<!doctype html>
<html><head><meta charset="utf-8"><link rel="stylesheet" href="STYLE"></head>
<body>
<div class="desktop-app"><div id="main"><div class="workspace-content">
<section class="panel form-panel" aria-label="Create VM">
  <div class="vm-fields">
    <label>Storage<select id="st"><option>local</option></select></label>
    <label>Disk name<input value="system"></label>
    <label>Size (GiB)<input type="number" value="20"></label>
    <label>Bus<select id="bus"><option value="virtio">VirtIO</option><option value="sata">SATA</option><option value="scsi" selected>SCSI</option></select></label>
    <label>Provisioning<select id="prov"><option>Thin</option></select></label>
  </div>
</section>
</div></div></div>
</body></html>
"""


class QuietHandler(SimpleHTTPRequestHandler):
    def log_message(self, *_):
        pass


def highlight_pixels(path, box):
    """Count picker pixels drawn below the control: the open picker's option rows."""
    from PIL import Image
    img = Image.open(path).convert("RGB")
    x0, y0 = int(box["x"]), int(box["y"] + box["height"] + 1)
    x1 = min(img.size[0], int(box["x"] + box["width"]))
    y1 = min(img.size[1], y0 + 120)
    return sum(1 for y in range(y0, y1) for x in range(x0, x1) if img.getpixel((x, y)) != (245, 249, 255))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", type=Path, default=ROOT / ".cache/form-select-picker")
    parser.add_argument("--headed", action="store_true")
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    checks = []

    def passed(message):
        checks.append(message)
        print("PASS:", message, flush=True)

    server = ThreadingHTTPServer(("127.0.0.1", 0), partial(QuietHandler, directory=str(ROOT / "web/src")))
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        from playwright.sync_api import sync_playwright
        base = f"http://127.0.0.1:{server.server_port}"
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=not args.headed)
            page = browser.new_page(viewport={"width": 900, "height": 700})
            page.set_content(SHELL.replace("STYLE", f"{base}/style.css"), wait_until="load")
            bus = page.locator("#bus")
            appearance = bus.evaluate("el => getComputedStyle(el).appearance")
            if appearance != "base-select":
                raise AssertionError(f"select appearance must be base-select so the picker renders in the page; got {appearance!r}")
            passed("Computed select appearance is base-select (in-page picker)")
            if bus.evaluate("el => el.matches(':open')"):
                raise AssertionError("Bus picker must be closed before the click")
            box = bus.bounding_box()
            bus.click()
            time.sleep(0.3)
            if not bus.evaluate("el => el.matches(':open')"):
                raise AssertionError("Bus picker did not open on click")
            passed("Bus picker is open 300 ms after the click")
            # React re-applies option.selected on every render of a controlled select;
            # the Controller reloads inventory every 10 s, so the picker must survive it.
            bus.evaluate("el => { for (const o of el.options) o.selected = o.value === 'scsi'; }")
            time.sleep(0.3)
            if not bus.evaluate("el => el.matches(':open')"):
                raise AssertionError("Bus picker closed when the options were re-selected (controlled re-render)")
            passed("Bus picker stays open across a controlled re-render of its options")
            shot = args.evidence / "bus-open.png"
            page.screenshot(path=str(shot))
            pixels = highlight_pixels(shot, box)
            if pixels < 2000:
                raise AssertionError(f"Open picker is not drawn below the control in the page capture ({pixels} pixels)")
            passed(f"Open picker captured in the page below the control ({pixels} pixels)")
            page.keyboard.press("ArrowUp")
            page.keyboard.press("Enter")
            if bus.evaluate("el => el.matches(':open')") or bus.evaluate("el => el.value") != "sata":
                raise AssertionError("Keyboard ArrowUp + Enter did not choose SATA and close the picker")
            passed("Keyboard ArrowUp + Enter chose SATA and closed the picker")
            browser.close()
        result = {
            "environment": "Controller web/src/style.css under desktop-shell markup; headless Chromium (Playwright)",
            "recorded_at": datetime.now(timezone.utc).isoformat(),
            "checks": checks,
            "limits": [
                "Proves the in-page picker under Chromium; browsers without base-select keep their native picker, which this check cannot exercise.",
            ],
        }
        (args.evidence / "results.json").write_text(json.dumps(result, indent=2) + "\n")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


if __name__ == "__main__":
    main()
