# GeeseFS image for the DaemonSet in geesefs-daemonset.yaml.
# Build from the repository root:
#
#   docker build -f contrib/k8s/Dockerfile -t geesefs:local .

FROM golang:1.25.9 AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o geesefs .

FROM ubuntu:24.04
# fuse3 provides fusermount3, which geesefs uses to unmount
RUN apt-get update \
    && apt-get install -y --no-install-recommends fuse3 ca-certificates \
    && rm -rf /var/lib/apt/lists/*
COPY --from=builder /build/geesefs /usr/local/bin/geesefs
ENTRYPOINT ["/usr/local/bin/geesefs"]
