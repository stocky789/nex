// SPDX-License-Identifier: AGPL-3.0-only
import { useState } from "react";
import type { VM } from "./managed";
import { guestNames } from "./vm-create";
import { Icon } from "./icons";

// Official, locally bundled artwork; guest input never becomes an asset URL.
const icons = new Map<string, [string, string]>([
  ["almalinux", ["almalinux.svg", "https://almalinux.org/"]],
  ["alpine", ["alpine.svg", "https://alpinelinux.org/"]],
  ["amzn", ["amazon-linux.svg", "https://aws.amazon.com/linux/"]],
  ["antix", ["antix.png", "https://antixlinux.com/"]],
  ["arch", ["arch.png", "https://archlinux.org/"]],
  ["aurora", ["aurora.svg", "https://getaurora.dev/"]],
  ["bazzite", ["bazzite-256.png", "https://bazzite.gg/"]],
  ["bluefin", ["bluefin.svg", "https://projectbluefin.io/"]],
  ["cachyos", ["cachyos.png", "https://cachyos.org/"]],
  ["centos", ["centos.svg", "https://www.centos.org/"]],
  ["debian", ["debian.svg", "https://www.debian.org/"]],
  ["deepin", ["deepin.ico", "https://www.deepin.org/"]],
  ["devuan", ["devuan.ico", "https://www.devuan.org/"]],
  ["elementary", ["elementary.svg", "https://elementary.io/"]],
  ["endeavouros", ["endeavouros.png", "https://endeavouros.com/"]],
  ["fedora", ["fedora.ico", "https://fedoraproject.org/"]],
  ["freebsd", ["freebsd-favicon.ico", "https://www.freebsd.org/"]],
  ["garuda", ["garuda.svg", "https://garudalinux.org/"]],
  ["gentoo", ["gentoo.svg", "https://www.gentoo.org/"]],
  ["kali", ["kali.svg", "https://www.kali.org/"]],
  ["linux", ["linux.svg", "https://www.kernel.org/"]],
  ["linuxmint", ["linuxmint.svg", "https://linuxmint.com/"]],
  ["mageia", ["mageia-favicon.png", "https://www.mageia.org/"]],
  ["manjaro", ["manjaro.svg", "https://manjaro.org/"]],
  ["mswindows", ["windows.svg", "https://www.microsoft.com/windows/"]],
  ["mx", ["mx-linux-logo.png", "https://mxlinux.org/"]],
  ["neon", ["kde-neon.svg", "https://neon.kde.org/"]],
  ["netbsd", ["netbsd-favicon.ico", "https://www.netbsd.org/"]],
  ["nixos", ["nixos.svg", "https://nixos.org/"]],
  ["nobara", ["nobara.svg", "https://nobaraproject.org/"]],
  ["ol", ["oracle-linux.png", "https://www.oracle.com/linux/"]],
  ["openbsd", ["openbsd-favicon.ico", "https://www.openbsd.org/"]],
  ["openmandriva", ["openmandriva.png", "https://www.openmandriva.org/"]],
  ["opensuse", ["opensuse.ico", "https://www.opensuse.org/"]],
  ["opensuse-leap", ["opensuse-leap.svg", "https://www.opensuse.org/"]],
  ["opensuse-tumbleweed", ["opensuse-tumbleweed.svg", "https://www.opensuse.org/"]],
  ["parrot", ["parrot.svg", "https://parrotsec.org/"]],
  ["pop", ["pop.svg", "https://pop.system76.com/"]],
  ["rhel", ["rhel.svg", "https://www.redhat.com/"]],
  ["rocky", ["rocky.svg", "https://rockylinux.org/"]],
  ["slackware", ["slackware.ico", "https://www.slackware.com/"]],
  ["sled", ["suse-sle.svg", "https://www.suse.com/"]],
  ["sles", ["suse-sle.svg", "https://www.suse.com/"]],
  ["solus", ["solus-symbol.svg", "https://getsol.us/"]],
  ["steamos", ["steamos.ico", "https://store.steampowered.com/steamos/"]],
  ["tails", ["tails-symbol.png", "https://tails.net/"]],
  ["ubuntu", ["ubuntu.svg", "https://ubuntu.com/"]],
  ["void", ["void.svg", "https://voidlinux.org/"]],
  ["windows", ["windows.svg", "https://www.microsoft.com/windows/"]],
  ["zorin", ["zorin.svg", "https://zorin.com/"]],
]);

export function GuestOS({ vm }: { vm: VM }) {
  const configured = vm.spec && guestNames[vm.spec.guest_os];
  const historical = !vm.guest?.os && vm.detected_os;
  const detected = vm.guest?.os ? vm.guest : vm.detected_os;
  const label = detected?.os ? `${detected.os}${historical ? " (last detected)" : ""}` : (configured ? `${configured} (configured)` : "OS unavailable");
  const family = vm.spec?.guest_os;
  const id = detected?.os_id || (!detected?.os ? (family === "linux" ? "linux" : family === "windows" ? "mswindows" : "") : "");
  const icon = icons.get(id.toLowerCase());
  const src = icon ? `/guest-os/${icon[0]}` : undefined;
  const [failed, setFailed] = useState<string>();
  const explanation = historical ? `Last detected by Core at ${historical.observed_at}. Current OS detection is unavailable; this does not indicate guest health.`
    : vm.guest?.os ? "Reported by QEMU Guest Agent." : "Detected OS unavailable. The configured family is a VM setting, not a detected distribution. OS details return when QEMU Guest Agent answers and the host reports them.";
  return <span className="guest-os" title={explanation}>{src && icon && failed !== src ?
    <a className="guest-os-icon" href={icon[1]} target="_blank" rel="noreferrer" aria-label={`${label} website (opens in a new tab)`} onClick={e => e.stopPropagation()}>
      <img src={src} width={18} height={18} alt="" onError={() => setFailed(src)} />
    </a> : <span className="guest-os-icon" aria-hidden="true"><Icon kind="vm" size={18} /></span>}
    <span>{label}</span></span>;
}
