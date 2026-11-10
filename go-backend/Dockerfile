FROM golang:1.25-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437 AS builder
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS
ARG TARGETARCH
# owner/repo whose releases the panel installs nodes and upgrades from.
ARG GITHUB_REPO
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} env ${TARGETARCH:+GOARCH=${TARGETARCH}} go build -ldflags="${GITHUB_REPO:+-X 'go-backend/internal/http/handler.githubRepo=${GITHUB_REPO}'}" -o /out/paneld ./cmd/paneld

FROM docker:29-cli@sha256:2357639ad1ea7d0194134bf3292622fd4e562483966ed674e5d7c945b5f3b28d AS dockercli

FROM debian:bookworm-slim@sha256:99c0b25c46d0214d5a2091092e7908907dcaecd6a962cde0ac700acee222b6f3
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates wget && rm -rf /var/lib/apt/lists/*
COPY --from=builder /out/paneld /app/paneld
COPY --from=dockercli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=dockercli /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose

ENV SERVER_ADDR=:6365
EXPOSE 6365
ENTRYPOINT ["/app/paneld"]
