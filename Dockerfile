FROM golang:1.23 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/isomedia ./cmd/isomedia && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /bin/isocli ./cmd/isocli

FROM ubuntu:24.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates \
      openssh-client \
      ffmpeg && \
    rm -rf /var/lib/apt/lists/*

COPY --from=builder /bin/isomedia /bin/isocli /usr/local/bin/
COPY web/ /app/web/

WORKDIR /app

RUN mkdir -p /var/lib/isomedia && chown 65532:65532 /var/lib/isomedia

USER 65532
EXPOSE 8080

ENTRYPOINT ["isomedia"]
