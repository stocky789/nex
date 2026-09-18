# SPDX-License-Identifier: AGPL-3.0-only
"""Synchronization for the storage browser fixtures."""
from time import monotonic


def take_deferred(page, pending, description):
    # UI busy/dialog state can render before the route callback runs. Pump
    # Playwright events until the actual request arrives, within its normal budget.
    deadline = monotonic() + 30
    while not pending and monotonic() < deadline:
        page.wait_for_timeout(25)
    assert pending, f"{description} was not requested within 30s"
    return pending.pop()

