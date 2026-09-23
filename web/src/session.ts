// SPDX-License-Identifier: AGPL-3.0-only
// Browser session hint for the login probe (BUG-0208); not an authorization.
const signedInKey = "nex-signed-in";

export function signedInBefore() {
  try { return localStorage.getItem(signedInKey) !== null; } catch { return true; }
}

export function rememberSignedIn(yes: boolean) {
  try { if (yes) localStorage.setItem(signedInKey, "1"); else localStorage.removeItem(signedInKey); } catch {}
}
