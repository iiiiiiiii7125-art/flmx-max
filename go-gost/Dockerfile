# Agent (node) image: the container install form of the node agent.
# Built and pushed by the docker-build workflow as flmx-agent alongside
# the panel images; the script install form does not use this image.
FROM golang:1.25-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437 AS builder
WORKDIR /src

COPY go.mod go.sum ./
# go.mod replaces github.com/go-gost/x with ./x, and `go mod download`
# reads the replaced module's go.mod — it must be in the layer before
# the download step. Keep in sync with the replace directives in go.mod.
COPY x/go.mod x/go.sum ./x/
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
# Release version stamped into the binary, same -X main.version
# convention as the gost release binaries in the workflow.
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} env ${TARGETARCH:+GOARCH=${TARGETARCH}} go build -ldflags="-s -w -X main.version=${VERSION}" -o /out/flmx-agent .

FROM debian:bookworm-slim@sha256:734a72b3d6142a71993b4bc580ca01d8ca687f77a2c422d96a961d3376b179bc
# Runtime tools the agent shells out to:
#   nft (nftables)     kernel nftables rules
#   modprobe (kmod)    tuning module loads
#   tcpkill (dsniff)   port release
#   ip/tc (iproute2), pkill (procps), iperf3
#                      the tuning engine's hard dependency list
#                      (prepareTuneEngine's LookPath set, together with
#                      bash/timeout/awk from the base image). They are
#                      preinstalled so tuning never has to apt-install
#                      at run time — an offline node or a read-only
#                      layer would otherwise fail tuning outright.
# This tool set is pinned by scripts/check-agent-image-tools.sh; do
# not drop a package without updating that gate.
#
# Container boundary: this image has no systemd. Tuning steps that
# persist through a systemd unit (flmx-tuning-qdisc.service) are
# skipped in container form, and runtime sysctl writes only take
# effect in privileged containers. Forwarding and nftables guards
# behave exactly as in the script form.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates nftables kmod dsniff iproute2 procps iperf3 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /out/flmx-agent /usr/local/bin/flmx-agent
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

# The agent reads ./config.json and persists its state (gost.json, seq
# file, downloaded kernels) in its working directory — the same layout
# as the script install's data directory. Install commands mount the
# host's own-named data directory here; the legacy shared directory of
# the script form is never used, so both forms (and other panels) can
# coexist on one host. The host-side directory may carry a per-panel
# instance suffix (for multiple panels on one host); the in-container
# path is always /etc/flmx_agent, and the entrypoint cd's here — the
# agent's data-directory anchor (self-uninstall wipe + marker) relies
# on that, so the entrypoint's cd target must not change.
WORKDIR /etc/flmx_agent

# Same runtime tuning knob as the script form's systemd unit.
ENV GODEBUG=disablethp=1

# Kernel state anchor: downloaded kernel binaries, their run configs,
# state.json and geodata live under the kernel root. Pin it inside
# the data directory (the mounted volume) so container rebuilds keep
# installed kernels; the built-in default root belongs to the script
# form's data directory, which in this image is only the ephemeral
# container layer. kernel.ResolveBaseDir also falls back to the
# working directory in container form if this is ever stripped, but
# the image pins it so every container start method agrees.
ENV FLMX_KERNEL_DIR=/etc/flmx_agent/kernels

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
