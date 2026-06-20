# syntax=docker/dockerfile:1
#
# Container image for `gk follow` — a zero-infra GitOps mirror: poll a remote
# branch, hard-reset the checkout to it on every advance, and run a hook. The
# image is a FOREGROUND process; supervise it (docker `--restart=always`, a k8s
# Deployment, systemd) rather than expecting a built-in daemon.

# --- build -------------------------------------------------------------------
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gk ./cmd/gk

# --- runtime -----------------------------------------------------------------
# gk follow shells out to git (and ssh for private remotes), so the runtime
# carries git + openssh-client + CA certs and nothing else. Hooks that need a
# toolchain (make, node, …) should `FROM ghcr.io/x-mesh/gk-follow` and add it.
FROM alpine:3.20
RUN apk add --no-cache git openssh-client ca-certificates
COPY --from=build /out/gk /usr/local/bin/gk

# Emit one JSON envelope per cycle so container logs are machine-readable.
ENV GK_AGENT=1
WORKDIR /repo

# `docker run img` → `gk follow main`. Override the branch and hook at run time:
#   docker run --rm -v "$PWD:/repo" -v ~/.ssh:/root/.ssh:ro \
#     ghcr.io/x-mesh/gk-follow main -- make deploy
ENTRYPOINT ["gk", "follow"]
CMD ["main"]
