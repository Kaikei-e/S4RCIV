# syntax=docker/dockerfile:1
FROM golang:1.26-trixie@sha256:0dcba0d95dbfb072e9917a106b9e07d7cc298097dc83e9307056ef1889de654d AS build
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
FROM gcr.io/distroless/static-debian12:nonroot@sha256:b7bb25d9f7c31d2bdd1982feb4dafcaf137703c7075dbe2febb41c24212b946f AS api
WORKDIR /app
COPY --from=build /out/api /app/api
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/api"]

# Command side (collector: discover / poll / project).
FROM gcr.io/distroless/static-debian12:nonroot@sha256:b7bb25d9f7c31d2bdd1982feb4dafcaf137703c7075dbe2febb41c24212b946f AS collector
WORKDIR /app
COPY --from=build /out/collector /app/collector
USER nonroot:nonroot
ENTRYPOINT ["/app/collector"]
