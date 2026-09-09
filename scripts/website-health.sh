#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-only
set -eu

: "${NEX_WEBSITE_URL:?set NEX_WEBSITE_URL in /etc/nex-website/health.env}"
: "${NEX_WEBSITE_EXPECTED:=Nex}"
body="$(curl --fail --silent --show-error --max-time 10 --max-filesize 1048576 "$NEX_WEBSITE_URL")"
printf '%s\n' "$body" | grep --fixed-strings --quiet "$NEX_WEBSITE_EXPECTED"
