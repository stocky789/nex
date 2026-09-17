# SPDX-License-Identifier: AGPL-3.0-only
FROM debian:13
RUN apt-get update && apt-get install -y --no-install-recommends gcc libc6-dev npm python3 dpkg-dev ca-certificates && rm -rf /var/lib/apt/lists/*
RUN python3 -c 'import urllib.request; urllib.request.urlretrieve("https://go.dev/dl/go1.27.1.linux-amd64.tar.gz", "/tmp/go.tar.gz")' && echo "63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445  /tmp/go.tar.gz" | sha256sum -c - && tar -C /usr/local -xzf /tmp/go.tar.gz && rm /tmp/go.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}" GOTOOLCHAIN=local
COPY build-inside.sh /usr/local/bin/build-nex-controller
ENTRYPOINT ["/usr/local/bin/build-nex-controller"]
