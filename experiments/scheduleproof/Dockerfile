FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -o /scheduleproof ./cmd/scheduleproof

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir /data && chown 65532:65532 /data
COPY --from=build /scheduleproof /usr/local/bin/scheduleproof
USER 65532:65532
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/scheduleproof"]
