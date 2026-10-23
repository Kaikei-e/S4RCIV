# syntax=docker/dockerfile:1
FROM golang:1.27.1-trixie@sha256:433790e515d27dc6003e847e644cc0af956985cf315c1c58a3b73ee2dd305183 AS build
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
FROM gcr.io/distroless/static-debian13:nonroot@sha256:d62067879de890a1def9da0e7766b7d20810a5e5555cef8506b752437d7a3e88 AS api
WORKDIR /app
COPY --from=build /out/api /app/api
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/api"]

# Command side (collector: discover / poll / project).
FROM gcr.io/distroless/static-debian13:nonroot@sha256:d62067879de890a1def9da0e7766b7d20810a5e5555cef8506b752437d7a3e88 AS collector
WORKDIR /app
COPY --from=build /out/collector /app/collector
USER nonroot:nonroot
ENTRYPOINT ["/app/collector"]
