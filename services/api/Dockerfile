# syntax=docker/dockerfile:1
FROM golang:1.27-trixie@sha256:ae28539d2ef595b9a2930dd7f031d9592376829dc0eae7cb869559f7d5812c3a AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Generated protobuf/connect code is committed under gen/, so no buf at build time.
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/collector ./cmd/collector

# Test stage: full Go toolchain + source for unit + integration tests. Reuses the
# build stage (source already COPYed, modules cached). CGO on so `go test -race`
# works (gcc ships in the golang image). The compose test-runner overrides CMD and
# mounts db/migrations at /migrations for the template-DB-per-test integration suite.
FROM build AS test
ENV CGO_ENABLED=1 MIGRATIONS_DIR=/migrations
CMD ["go", "test", "./..."]

# Query side (read-only Connect-RPC server).
FROM gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7 AS api
WORKDIR /app
COPY --from=build /out/api /app/api
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/api"]

# Command side (collector: discover / poll / project).
FROM gcr.io/distroless/static-debian13:nonroot@sha256:1c2c046bc09ed40fad370b599a0b1ae7987f55b01e247cf27a7c27cd97e5bbc7 AS collector
WORKDIR /app
COPY --from=build /out/collector /app/collector
USER nonroot:nonroot
ENTRYPOINT ["/app/collector"]
