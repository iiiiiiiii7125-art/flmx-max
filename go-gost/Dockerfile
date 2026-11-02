# Agent (node) image: the container install form of the node agent.
# Built and pushed by the docker-build workflow as flmx-agent alongside
# the panel images; the script install form does not use this image.
FROM golang:1.25-bookworm AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
# Release version stamped into the binary, same -X main.version
# convention as the gost release binaries in the workflow.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} env ${TARGETARCH:+GOARCH=${TARGETARCH}} go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/flmx-agent .

FROM debian:bookworm-slim
# Runtime tools the agent shells out to: nft (kernel nftables rules),
# modprobe (tuning module loads), tcpkill (port release). procps is not
# needed: tuning writes /proc/sys directly.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates nftables kmod dsniff \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/flmx-agent /usr/local/bin/flmx-agent
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# The agent reads ./config.json and persists its state (gost.json, seq
# file, downloaded kernels) in its working directory — the same layout
# as the script install's data directory. Install commands mount the
# host's own-named data directory here; the legacy shared directory of
# the script form is never used, so both forms (and other panels) can
# coexist on one host.
WORKDIR /etc/flmx_agent

# Same runtime tuning knob as the script form's systemd unit.
ENV GODEBUG=disablethp=1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
