# Builds nomctl-relay from source. The release workflow publishes prebuilt
# images to ghcr.io/0x3639/nomctl-relay; this file is for local builds.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X github.com/0x3639/nomctl/internal/relay.Version=${VERSION}" -o /nomctl-relay ./cmd/nomctl-relay

FROM gcr.io/distroless/static:nonroot
COPY --from=build /nomctl-relay /nomctl-relay
USER 65532:65532
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/nomctl-relay", "-healthcheck"]
ENTRYPOINT ["/nomctl-relay"]
