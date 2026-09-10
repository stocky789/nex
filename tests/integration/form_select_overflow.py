#!/usr/bin/env python3
# SPDX-License-Identifier: AGPL-3.0-only
"""BUG-0050: form panels must not clip native <select> menus.

Headless Chromium does not reproduce the flicker, so CI asserts the Controller
stylesheet keeps .panel overflow visible under the desktop shell. Pass --headed
(or NEX_FORM_SELECT_HEADED=1 with DISPLAY) to also require the open-menu highlight.
"""
import argparse
from datetime import datetime, timezone
from functools import partial
from http.server import SimpleHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
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
    <label>Bus<select id="bus"><option>VirtIO</option><option>SATA</option><option selected>SCSI</option></select></label>
    <label>Provisioning<select id="prov"><option>Thin</option></select></label>
  </div>
</section>
</div></div></div>
</body></html>
"""


class QuietHandler(SimpleHTTPRequestHandler):
    def log_message(self, *_):
        pass


def assert_source_rule():
    css = (ROOT / "web/src/style.css").read_text()
    match = re.search(r"(?<![\w-])\.panel\s*\{([^}]+)\}", css)
    if not match:
        raise AssertionError(".panel rule missing from web/src/style.css")
    body = re.sub(r"/\*.*?\*/", "", match.group(1), flags=re.S)
    if re.search(r"overflow\s*:\s*(hidden|auto|scroll|clip)\b", body):
        raise AssertionError(
            ".panel must not set a clipping overflow; native <select> menus close immediately "
            "inside the desktop shell (BUG-0050). Got: " + body.strip()
        )


def below_menu_blues(path, box):
    from PIL import Image
    img = Image.open(path).convert("RGB")
    x0, y0 = int(box["x"]), int(box["y"] + box["height"] + 1)
    x1 = min(img.size[0], int(box["x"] + max(box["width"], 160)))
    y1 = min(img.size[1], y0 + 140)
    blues = 0
    for y in range(y0, y1):
        for x in range(x0, x1):
            r, g, b = img.getpixel((x, y))
            if b > 150 and r < 100 and g < 160:
                blues += 1
    return blues


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", type=Path, default=ROOT / ".cache/form-select-overflow")
    parser.add_argument("--headed", action="store_true", help="Require a visible open select menu")
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    assert_source_rule()
    print("PASS: source .panel rule has no clipping overflow", flush=True)

    headed = args.headed or (bool(os.environ.get("DISPLAY")) and os.environ.get("NEX_FORM_SELECT_HEADED") == "1")
    server = ThreadingHTTPServer(("127.0.0.1", 0), partial(QuietHandler, directory=str(ROOT / "web/src")))
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        from playwright.sync_api import sync_playwright
        base = f"http://127.0.0.1:{server.server_port}"
        checks = ["source .panel rule has no clipping overflow"]
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=not headed)
            page = browser.new_page(viewport={"width": 900, "height": 700})
            page.set_content(SHELL.replace("STYLE", f"{base}/style.css"), wait_until="load")
            overflow = page.locator(".panel").evaluate("el => getComputedStyle(el).overflow")
            if overflow != "visible":
                raise AssertionError(f".panel computed overflow must be visible; got {overflow!r}")
            checks.append(f"Computed .panel overflow is visible ({overflow})")
            print(f"PASS: Computed .panel overflow is visible ({overflow})", flush=True)

            bus = page.locator("#bus")
            box = bus.bounding_box()
            bus.click()
            time.sleep(0.45)
            shot = args.evidence / ("bus-open-headed.png" if headed else "bus-after-click.png")
            page.screenshot(path=str(shot))
            if headed:
                blues = below_menu_blues(shot, box)
                if blues < 500:
                    raise AssertionError(
                        f"Bus select menu did not stay open after click (highlight pixels below control: {blues})"
                    )
                checks.append(f"Headed Bus menu stayed open ({blues} highlight pixels)")
                print(f"PASS: Headed Bus menu stayed open ({blues} highlight pixels)", flush=True)
            else:
                msg = "Headless click captured; menu-pixel check skipped (Chromium headless does not reproduce BUG-0050)"
                checks.append(msg)
                print("PASS:", msg, flush=True)
            browser.close()
        result = {
            "environment": "Controller web/src/style.css under desktop-shell markup; Chromium Playwright",
            "recorded_at": datetime.now(timezone.utc).isoformat(),
            "headed": headed,
            "checks": checks,
            "limits": [
                "Headless CI asserts stylesheet/computed overflow only; run with --headed for the open-menu pixel check.",
            ],
        }
        (args.evidence / "results.json").write_text(json.dumps(result, indent=2) + "\n")
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=5)


if __name__ == "__main__":
    main()
