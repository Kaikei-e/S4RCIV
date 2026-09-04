# syntax=docker/dockerfile:1
# Builder is Debian 13 (trixie) to match the glibc of the distroless runtime.
FROM rust:1.98.0-trixie@sha256:620dbcd124499c59e2406d3741574b5c5838cf9eb9656f0c3a03948f79b02959 AS build
# protoc is required by build.rs (prost-build invokes it to compile the proto).
RUN apt-get update \
    && apt-get install -y --no-install-recommends protobuf-compiler \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY Cargo.toml Cargo.lock ./
COPY build.rs ./
COPY proto ./proto
COPY src ./src
RUN cargo build --release && cp target/release/differ /differ

# Test stage: unit + the in-process diff/eId contract tests (tests/diff_contract.rs).
# The build stage does not copy tests/, so bring it in here. Reuses the build stage's
# cargo registry, so `cargo test` needs no network.
FROM build AS test
COPY tests ./tests
CMD ["cargo", "test", "--all"]

FROM gcr.io/distroless/cc-debian13:nonroot@sha256:c31ff9abcb1910f3ab25c7957bdaf0bfe12a01eb546e8df2282f1c8f682b606c
COPY --from=build /differ /app/differ
# The Connect-RPC server binds 0.0.0.0:$DIFFER_PORT (default 9090).
EXPOSE 9090
USER nonroot:nonroot
ENTRYPOINT ["/app/differ"]
