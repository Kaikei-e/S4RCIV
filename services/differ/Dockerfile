# syntax=docker/dockerfile:1
# Builder is Debian 12 (bookworm) to match the glibc of the distroless runtime.
FROM rust:1.97.1-bookworm@sha256:0e2bcaef56d041a486784e54104a81aebe0da44bd03019bd70bc0401e42e4a97 AS build
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

FROM gcr.io/distroless/cc-debian12:nonroot@sha256:66aa873a4a14fb164aa01296058efd8253744606d72715e45acface073359faa
COPY --from=build /differ /app/differ
# The Connect-RPC server binds 0.0.0.0:$DIFFER_PORT (default 9090).
EXPOSE 9090
USER nonroot:nonroot
ENTRYPOINT ["/app/differ"]
