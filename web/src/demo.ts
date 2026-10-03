// SPDX-License-Identifier: AGPL-3.0-only
import "./main";
import "./demo.css";

// Only the demo build imports this entry; the Controller UI remains shared.
document.documentElement.classList.add("public-demo");
const banner = document.createElement("aside");
banner.className = "demo-banner";
banner.setAttribute("aria-label", "Public demo information");
const title = document.createElement("strong");
title.textContent = "Public demo";
const description = document.createElement("span");
description.textContent = "Fictional data · Read-only · Sign in with demo / demo";
const source = document.createElement("a");
source.href = "/source";
source.textContent = "Source code";
banner.append(title, description, source);
document.body.prepend(banner);
new ResizeObserver(() => {
  document.documentElement.style.setProperty("--demo-banner-height", `${banner.offsetHeight}px`);
}).observe(banner);
