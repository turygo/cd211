FROM --platform=$BUILDPLATFORM golang:1.26.5-bookworm@sha256:6c5605ab3a9a9fb3c4eafe5b3d63cdbf3881caf113262b67862547b54a9db599 AS build

ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Cross-compile from the native builder arch; QEMU-emulating the Go build costs
# roughly ten times the wall time for the same output.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w -X github.com/turygo/cd211/internal/buildinfo.Version=$VERSION" -o /cd211 ./cmd/cd211

FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

# su-exec drops to the deployment's UID/GID; ca-certificates is required for the
# default TLS gRPC connection to CloudDrive2.
RUN apk add --no-cache ca-certificates su-exec \
    && mkdir -p /data /downloads

COPY --from=build /cd211 /usr/local/bin/cd211
COPY docker-entrypoint.sh /usr/local/bin/
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 8080
VOLUME ["/data", "/downloads"]

# /healthz only checks the SQLite store, so an unreachable CloudDrive2 never
# restarts the container. The probe assumes the default listen port.
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["docker-entrypoint.sh"]
CMD ["cd211"]
