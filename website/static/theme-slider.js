// SPDX-License-Identifier: AGPL-3.0-only
// Gallery light/dark sliders: the range input sets --pos, which the stylesheet uses to clip the dark image.
for (const slider of document.querySelectorAll(".theme-slider")) {
  const input = slider.querySelector("input");
  input.addEventListener("input", () => slider.style.setProperty("--pos", input.value + "%"));
}
