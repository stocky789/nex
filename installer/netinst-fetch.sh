#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
# The ISO pins every byte; URLs alone never authorize an installation payload.
# Rows: SHA-256, destination below the target directory, HTTPS URL. NEX_NETINST_ROWS
# selects the destination prefixes this call handles: image/ and payload/ (the default).
set -eu
manifest=${NEX_NETINST_MANIFEST:-/cdrom/nex/payloads.tsv}
mode=${1:?expected --check or a destination directory}
rows=${NEX_NETINST_ROWS:-image/ payload/}
checked=
while IFS="$(printf '\t')" read -r digest relative url; do
    case "$relative" in image/*|payload/*/*) ;; *) echo 'Invalid netinst destination' >&2; exit 1;; esac
    case "$relative" in *..*|*[!a-zA-Z0-9_./+%:~-]*) echo 'Invalid netinst path' >&2; exit 1;; esac
    case "$url" in https://*) ;; *) echo 'Netinst requires HTTPS sources' >&2; exit 1;; esac
    selected=
    for prefix in $rows; do
        case "$relative" in "$prefix"*) selected=yes;; esac
    done
    [ -n "$selected" ] || continue
    if [ "$mode" = --check ]; then
        # Check each Nex blob, plus one file per upstream host.
        case "$url" in
            https://fedorapeople.org/*) source=fedora ;;
            *) source=$relative ;;
        esac
        case " $checked " in *" $source "*) continue;; esac
        checked="$checked $source"
        wget -q -t 1 -T 30 --spider "$url" || { echo 'Net installer download unavailable; check network and retry.' >&2; exit 1; }
        continue
    fi
    destination=$mode/$relative
    mkdir -p "$(dirname "$destination")"
    if [ -f "$destination" ] && printf '%s  %s\n' "$digest" "$destination" | sha256sum -c - >/dev/null 2>&1; then continue; fi
    printf 'Downloading %s\n' "$relative"
    attempt=0
    while :; do
        if wget -q -t 1 -T 60 -c -O "$destination.partial" "$url"; then
            if printf '%s  %s\n' "$digest" "$destination.partial" | sha256sum -c -; then break; fi
            # Transport interruptions can resume; complete but untrusted bytes cannot.
            rm -f "$destination.partial"
        fi
        attempt=$((attempt + 1))
        [ "$attempt" -lt 3 ] || { echo "Net installer download failed verification: $relative" >&2; exit 1; }
    done
    mv "$destination.partial" "$destination"
done < "$manifest"
