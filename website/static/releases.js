// SPDX-License-Identifier: AGPL-3.0-only
// Download cards: each card's data-manifest is the latest.json that the release publisher
// rewrites on every release, so new builds appear without redeploying the site. Without this
// script, or if the manifest cannot be read, the cards keep their links to the downloads pages.
const size = (bytes) => (bytes >= 1e9 ? (bytes / 1e9).toFixed(1) + " GB" : Math.round(bytes / 1e6) + " MB");

async function fill(card) {
  const url = new URL(card.dataset.manifest, location.href);
  const response = await fetch(url, { headers: { Accept: "application/json" } });
  if (!response.ok) throw new Error(url + " returned " + response.status);
  const { version, published, files } = await response.json();
  const { iso, netinst } = files;
  if (typeof version !== "string" || typeof iso?.path !== "string") throw new Error("unexpected manifest");

  card.querySelector("[data-version]").textContent = "v" + version;
  card.querySelector("[data-meta]").textContent =
    ["Nex Core installer", typeof iso.size === "number" && size(iso.size), published && "built " + published]
      .filter(Boolean)
      .join(" · ");
  const primary = card.querySelector("[data-iso]");
  primary.href = new URL(iso.path, url);
  primary.querySelector("[data-iso-label]").textContent = "Download ISO";
  if (typeof netinst?.path === "string") {
    const link = card.querySelector("[data-netinst]");
    link.href = new URL(netinst.path, url);
    link.hidden = false;
  }
  if (/^[0-9a-f]{64}$/.test(iso.sha256)) {
    const hash = card.querySelector("[data-hash]");
    hash.textContent = "SHA-256 " + iso.sha256;
    hash.hidden = false;
  }
}

for (const card of document.querySelectorAll("[data-manifest]")) {
  fill(card).catch(() => {});
}
