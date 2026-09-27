# Management DHCP display check

Date: 2026-09-25. Environment: local development checkout, Chromium headless through the existing browser QA virtual environment. The browser served a temporary bundled copy of the real `HostNetworkPanel` with a fictional Core response; it did not contact a host or DHCP server. Captures are in ignored `.cache/dhcp-qa/` and were visually inspected, not added to the public repository.

- Fresh DHCP report: disabled Address and Gateway boxes showed `192.0.2.10/24` and `192.0.2.1` from the reported bridge and route; inspected `fresh.png`.
- Switched Addressing to Static and back: boxes cleared for Static and returned to the observed values for DHCP.
- Failed report: both DHCP boxes showed *Not reported*; inspected `error.png`. Stale report: form was absent and the state was unknown; inspected `stale.png`.
- Focused UI, Core gateway parser, Controller projection, protocol and API contract checks passed. A real DHCP lease, Core/Controller integration with different installed versions, and a full host-panel browser flow were not exercised for this change.
