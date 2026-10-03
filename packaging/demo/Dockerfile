# SPDX-License-Identifier: AGPL-3.0-only
FROM node:24-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run typecheck && node build.mjs --demo

FROM golang:1.27.1-bookworm AS build
RUN apt-get update && apt-get install -y --no-install-recommends python3 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -o /out/nex-demo ./cmd/nex-demo
RUN python3 packaging/demo/build-source.py --output /out/source.tar.gz

FROM scratch
ARG NEX_DEMO_REVISION
LABEL org.opencontainers.image.title="Nex read-only simulated demo" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.revision="${NEX_DEMO_REVISION}"
COPY --from=build /out/nex-demo /app/nex-demo
COPY --from=build /out/source.tar.gz /app/source.tar.gz
COPY --from=web /src/web/dist /app/web
COPY --from=build /src/docs/api /app/docs
USER 64050:64050
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 CMD ["/app/nex-demo", "-healthcheck", "-socket", "/run/demo/demo.sock"]
ENTRYPOINT ["/app/nex-demo"]
CMD ["-socket", "/run/demo/demo.sock", "-ui-dir", "/app/web", "-docs-dir", "/app/docs", "-source-archive", "/app/source.tar.gz", "-public-origin", "https://demo.example.com"]
