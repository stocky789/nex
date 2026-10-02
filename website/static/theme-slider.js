// SPDX-License-Identifier: AGPL-3.0-only
// Gallery light/dark sliders: the range input sets --pos, which the stylesheet uses to clip the dark image.
// The input ships disabled so that it is inert without this script.
for (const slider of document.querySelectorAll(".theme-slider")) {
  const input = slider.querySelector("input");
  const update = () => {
    slider.style.setProperty("--pos", input.value + "%");
    input.setAttribute("aria-valuetext", input.value + "% light, " + (100 - input.value) + "% dark");
  };
  input.disabled = false;
  update();
  input.addEventListener("input", update);
}
