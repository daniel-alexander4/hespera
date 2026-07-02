FROM golang:1.23 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/hespera ./cmd/hespera && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/hescli ./cmd/hescli

FROM ubuntu:24.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      ffmpeg && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /bin/hespera /bin/hescli /usr/local/bin/
COPY web/ /app/web/
# Ensure assets are world-readable (dirs traversable) regardless of the host
# umask at build time, so the non-root runtime user can serve them.
RUN chmod -R a+rX /app/web

WORKDIR /app

RUN mkdir -p /var/lib/hespera && chown 65532:65532 /var/lib/hespera

USER 65532
EXPOSE 8080

ENTRYPOINT ["hespera"]
