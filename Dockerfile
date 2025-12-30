# syntax=docker/dockerfile:1

FROM golang:1.24-bookworm AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" -o /out/spaghetti ./cmd

FROM debian:bookworm-slim AS runner

RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /var/lib/spaghetti

COPY --from=builder /out/spaghetti /usr/local/bin/spaghetti

RUN groupadd --system spaghetti && useradd --system --gid spaghetti --home-dir /var/lib/spaghetti spaghetti && \
    mkdir -p /var/lib/spaghetti && chown -R spaghetti:spaghetti /var/lib/spaghetti

USER spaghetti:spaghetti

EXPOSE 6000

VOLUME ["/var/lib/spaghetti"]

ENTRYPOINT ["spaghetti"]
CMD ["-listenaddr", ":6000"]
