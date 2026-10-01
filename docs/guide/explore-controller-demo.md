# Explore the public Controller demo

## Status

The public demo uses the current Nex Controller interface with fictional sites, pools,
hosts, virtual machines, storage, networks and backup data. It is a **read-only browsing
demo**, not a connection to real Nex Core hosts. Sample utilization, jobs, recovery
points and verification results are illustrations, not evidence of actual workloads
or successful restores. Features that are unavailable in Controller remain unavailable.

The shared credentials are **Username: `demo`** and **Password: `demo`**, displayed
directly on the demo login card. They grant access only to this separate demo service,
not to a normal Controller installation. Management requests, host
connections, uploads, consoles, backup execution and restores are refused by the demo
server. Do not enter real credentials or confidential information in demo forms.

## Prerequisites

- The demo address supplied by the website operator, served over HTTPS.
- A current browser with JavaScript enabled.
- No Nex installation, host connection, account registration or payment is required.

## Steps

1. Open the demo address. Confirm the **Public demo** notice says that the data is
   fictional and read-only.
2. Enter `demo` in **Username** and `demo` in **Password**, then choose **Sign in**.
3. Use the top-bar scope menu to choose a site and pool. Inspect **Overview** and use
   **VMs** or **Hosts** to select a resource and open its detail panel. Search and
   filters operate on the sample inventory.
   The fictional inventory includes 44 VMs and 11 hosts across three pools. Group
   VMs by **Tag** to see colorful workload, environment and service labels.
4. Open **Storage** and **Networks** to inspect registrations. Open **Backups** to
   browse **Backup jobs** and **Recovery points**. Inspect a job or point; these
   records are fictional and cannot create, restore or delete data.
5. Open **Settings** to inspect the sample configuration, including backup
   repositories. **Tasks** and **Events** show illustrative operation history.
   Use the theme control to inspect light and dark appearance.
   Under **Settings → Members**, open the pool status **About** details to inspect
   the simulated quorum/watchdog cluster. Production and edge examples are mock-armed;
   the research example is off. These are illustrations, never hardware or HA proof.
6. Open the account menu and choose **Sign out** when finished. The **Source code**
   link downloads the corresponding source for the deployed demo build.

## Expected result

The same interface used by Controller renders populated sample views. Browsing,
searching, filtering and opening panels do not change shared inventory. Submitting a
management action produces a read-only refusal, not a successful operation. No VM,
host, repository or backup is affected.

The demo is rebuilt from the shared UI when the configured Git branch changes; it is
not a promise that every future API change automatically has meaningful sample data.
Build checks and browsing checks must catch contract drift before publication.

## Troubleshooting

- **Sign-in fails:** use the exact shared credentials above. A rate-limit response
  means too many requests; wait before retrying, rather than repeatedly submitting.
- **A management action is refused:** expected. The public demo is for inspection,
  not a writable sandbox. Its server does not implement real host operations.
- **A console is unavailable:** expected. No real guest or host console exists.
- **The session expires after a deployment or restart:** sign in again. Demo sessions
  are disposable; fictional inventory is supplied by the new build.
- **The address or HTTPS certificate is unavailable:** the website operator must check
  DNS and the HTTPS proxy route. Do not bypass certificate errors on the public site.
- **A page cannot load sample data:** report the page and error to the website
  operator. Do not supply real environment information to populate it.

## Verification

Deployment, API and browser verification are recorded in the
[public demo verification report](../qa/2026-10-01-public-controller-demo.md).
External DNS and HTTPS routing are separate acceptance checks and must be completed
by the website operator before advertising the public address.
