# SPDX-License-Identifier: AGPL-3.0-only
# This builds a separately distributed Debian DLM package; it does not combine
# DLM's GPL-2-or-later code with Nex.
FROM debian:13

RUN printf '%s\n' '#!/bin/sh' 'exit 101' > /usr/sbin/policy-rc.d \
 && chmod 755 /usr/sbin/policy-rc.d \
 && printf '%s\n' 'deb-src http://deb.debian.org/debian trixie main' > /etc/apt/sources.list.d/nex-dlm.sources.list \
 && apt-get update \
 && apt-get install -y --no-install-recommends build-essential ca-certificates devscripts dpkg-dev fakeroot \
 && apt-get build-dep -y --no-install-recommends dlm=4.3.0-3 \
 && apt-get install -y --no-install-recommends pacemaker-dev=3.0.0-2 \
 && dpkg-query -W -f='${Version}\n' pacemaker-dev | grep -Fx 3.0.0-2 \
 && rm -rf /var/lib/apt/lists/*

COPY --chmod=755 build-matched-dlm.sh /usr/local/bin/build-matched-dlm
ENTRYPOINT ["/usr/local/bin/build-matched-dlm"]
