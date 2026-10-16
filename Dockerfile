# SPDX-License-Identifier: Apache-2.0
FROM golang:1.27.2-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=0.1.0-alpha.1
ARG COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -buildid= -X main.version=${VERSION} -X main.commit=${COMMIT}" -o /out/rendercache ./cmd/rendercache

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends chromium chromium-sandbox ca-certificates fonts-liberation tini \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 rendercache \
    && useradd --uid 10001 --gid 10001 --home-dir /tmp --no-create-home --shell /usr/sbin/nologin rendercache \
    && mkdir -p /data && chown rendercache:rendercache /data
COPY --from=build /out/rendercache /usr/local/bin/rendercache
COPY deploy/container.json /etc/rendercache.json
COPY LICENSE THIRD_PARTY_NOTICES /usr/share/doc/rendercache/
USER 10001:10001
ENV HOME=/tmp
WORKDIR /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD ["rendercache", "health"]
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/rendercache"]
CMD ["serve", "--config", "/etc/rendercache.json"]
