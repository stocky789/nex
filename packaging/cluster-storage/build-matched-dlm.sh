#!/bin/bash
# SPDX-License-Identifier: AGPL-3.0-only
# Build a local Debian DLM package with the Pacemaker 3 fencing-helper ABI.
set -euo pipefail

readonly dlm_version=4.3.0-3
readonly pacemaker_version=3.0.0-2
readonly stonith_package=libstonithd56
readonly local_version="${dlm_version}+nex1"
readonly output=/output
readonly source_dir=/build/source
readonly unpacked=/build/dlm-${dlm_version}
readonly pacemaker_source_dir=/build/pacemaker-source
readonly built_utc="$(date -u +%FT%TZ)"

test -d "$output"
install -d -m 0755 "$source_dir" "$output/source" "$output/notices"
exec > >(tee "$output/build.log") 2>&1
cd "$source_dir"
apt-get update
apt-get source --download-only "dlm=${dlm_version}"
mapfile -t source_files < <(find . -maxdepth 1 -type f \( -name "dlm_${dlm_version}.dsc" -o -name 'dlm_4.3.0.orig.*' -o -name "dlm_${dlm_version}.debian.tar.*" \) -printf '%f\n' | sort)
[[ ${#source_files[@]} == 3 ]] || { echo 'unexpected Debian dlm source artifact set' >&2; exit 1; }
for source in "${source_files[@]}"; do
    test -f "$source"
    install -m 0644 "$source" "$output/source/$source"
done
install -d -m 0755 "$pacemaker_source_dir"
cd "$pacemaker_source_dir"
apt-get source --download-only "pacemaker=${pacemaker_version}"
mapfile -t pacemaker_source_files < <(find . -maxdepth 1 -type f \( -name "pacemaker_${pacemaker_version}.dsc" -o -name 'pacemaker_3.0.0.orig.*' -o -name "pacemaker_${pacemaker_version}.debian.tar.*" \) -printf '%f\n' | sort)
[[ ${#pacemaker_source_files[@]} == 3 ]] || { echo 'unexpected Debian Pacemaker source artifact set' >&2; exit 1; }
for source in "${pacemaker_source_files[@]}"; do
    install -m 0644 "$source" "$output/source/$source"
done
install -m 0644 /usr/include/pacemaker/crm/stonith-ng.h "$output/source/pacemaker-stonith-ng.h.original"
python3 - <<'PY'
from pathlib import Path
original = Path('/usr/include/pacemaker/crm/stonith-ng.h').read_text()
old = '#  define STONITH_LIBRARY "libstonithd.so.26"'
new = '#  define STONITH_LIBRARY "libstonithd.so.56"'
if original.count(old) != 1 or new in original:
    raise SystemExit('unexpected Pacemaker stonith helper SONAME literal')
Path('/output/source/pacemaker-stonith-ng.h.overlay').write_text(original.replace(old, new, 1))
PY
diff -u "$output/source/pacemaker-stonith-ng.h.original" "$output/source/pacemaker-stonith-ng.h.overlay" > "$output/source/pacemaker-stonith-library-soname.patch" || test $? -eq 1
cd "$source_dir"
dpkg-source -x "dlm_${dlm_version}.dsc" "$unpacked"
cd "$unpacked"

# Pacemaker's public inline helper supplies the dlopen SONAME compiled into
# dlm_stonith. It is .26 despite the exact installed runtime being .56.
# Generate an overlay before its pkg-config include; do not alter the installed
# Pacemaker header or install an ABI alias.
dpkg-query -W -f='${Version}\n' pacemaker-dev | grep -Fx "$pacemaker_version"
dpkg-query -W -f='${Version}\n' "$stonith_package" | grep -Fx "$pacemaker_version"
grep -Fqx '#  define STONITH_LIBRARY "libstonithd.so.26"' /usr/include/pacemaker/crm/stonith-ng.h
python3 - <<'PY'
from pathlib import Path
control = Path('debian/control')
text = control.read_text()
needle = 'Depends:\n corosync,\n kmod,\n'
replacement = 'Depends:\n corosync,\n kmod,\n libstonithd56 (= 3.0.0-2),\n'
if needle not in text or replacement in text:
    raise SystemExit('unexpected Debian dlm control layout')
control.write_text(text.replace(needle, replacement, 1))
overlay = Path('debian/nex-pacemaker-overlay.py')
overlay.write_text('''from pathlib import Path
source = Path('/usr/include/pacemaker/crm/stonith-ng.h').read_text()
old = '#  define STONITH_LIBRARY "libstonithd.so.26"'
new = '#  define STONITH_LIBRARY "libstonithd.so.56"'
if source.count(old) != 1 or new in source:
    raise SystemExit('unexpected Pacemaker stonith helper SONAME literal')
target = Path('debian/nex-pacemaker-overlay/crm/stonith-ng.h')
target.parent.mkdir(parents=True, exist_ok=True)
target.write_text(source.replace(old, new, 1))
''')
rules = Path('debian/rules')
rules_text = rules.read_text()
if 'override_dh_auto_build:' in rules_text:
    raise SystemExit('unexpected Debian dlm rules override')
rules.write_text(rules_text + '''
# Build-local Pacemaker fencing-helper SONAME compatibility overlay.
override_dh_auto_build:
\tpython3 debian/nex-pacemaker-overlay.py
\tdh_auto_build -- PACEMAKER_CFLAGS="-I$(CURDIR)/debian/nex-pacemaker-overlay $(shell pkg-config --cflags pacemaker-fencing)"
''')
PY
DEBFULLNAME='Nex packaging' DEBEMAIL='noreply@example.invalid' \
    dch --newversion "$local_version" --distribution trixie 'Rebuild against Pacemaker 3 fencing helper ABI.'
install -m 0644 debian/copyright "$output/notices/dlm-copyright"
dpkg-buildpackage -us -uc -b
cd /build
for deb in dlm-controld_${local_version}_*.deb; do
    test -f "$deb"
    install -m 0644 "$deb" "$output/$(basename "$deb")"
done

# Retain the exact custom source package and packaging delta, alongside the
# untouched upstream Debian source artifacts above.
cd "$unpacked"
dpkg-buildpackage -us -uc -S
cd /build
for source in dlm_${local_version}.dsc dlm_${local_version}.debian.tar.*; do
    test -f "$source"
    install -m 0644 "$source" "$output/source/$source"
done
printf '%s\n' \
    "dlm_source_version=${dlm_version}" \
    "local_binary_version=${local_version}" \
    "pacemaker_dev_version=${pacemaker_version}" \
    "runtime_package=${stonith_package}" \
    "runtime_version=${pacemaker_version}" \
    "built_utc=${built_utc}" > "$output/build-inputs.txt"

deb=$(find "$output" -maxdepth 1 -name 'dlm-controld_*.deb' -type f -print -quit)
test -n "$deb"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
dpkg-deb -x "$deb" "$work/root"
helper="$work/root/usr/sbin/dlm_stonith"
test -x "$helper"
strings "$helper" | grep -Fx 'libstonithd.so.56'
! strings "$helper" | grep -Fq 'libstonithd.so.26'
nm -D /usr/lib/$(dpkg-architecture -qDEB_HOST_MULTIARCH)/libstonithd.so.56 | grep -Eq ' T stonith_api_kick$'
nm -D /usr/lib/$(dpkg-architecture -qDEB_HOST_MULTIARCH)/libstonithd.so.56 | grep -Eq ' T stonith_api_time$'
cat > "$work/dlopen-smoke.c" <<'C'
#include <dlfcn.h>
int main(void) {
    void *library = dlopen("libstonithd.so.56", RTLD_NOW | RTLD_LOCAL);
    if (!library) return 1;
    if (!dlsym(library, "stonith_api_kick") || !dlsym(library, "stonith_api_time")) return 2;
    return dlclose(library);
}
C
cc -Wall -Wextra -Werror "$work/dlopen-smoke.c" -ldl -o "$work/dlopen-smoke"
"$work/dlopen-smoke"
dpkg-deb -f "$deb" Depends | grep -Fq 'libstonithd56 (= 3.0.0-2)'
# build.log is still the active tee target, so retain it but exclude it from
# checksums; all immutable package, source, notice and input files are listed.
(cd "$output" && find . -type f ! -name SHA256SUMS ! -name build.log -printf '%P\0' | sort -z | xargs -0 sha256sum > SHA256SUMS)
