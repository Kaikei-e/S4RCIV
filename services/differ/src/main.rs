//! Differ service entry point: wire config + router, bind, serve with graceful
//! shutdown. All logic lives in the library crate.

use std::net::{Ipv4Addr, SocketAddr};
use std::time::Duration;

use differ::config::Config;
use differ::connect;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tracing::info;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    // Container healthcheck entry point (`differ --healthcheck`): the distroless
    // image ships no shell/curl, so the container's HEALTHCHECK CMD must be this
    // binary itself — self-probe the /healthz route it already serves, mirroring
    // cmd/api's `-healthcheck` flag on the Go side.
    if std::env::args().nth(1).as_deref() == Some("--healthcheck") {
        std::process::exit(healthcheck().await);
    }

    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env()
                .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("info")),
        )
        .init();

    let config = Config::from_env();
    let addr = SocketAddr::from((Ipv4Addr::UNSPECIFIED, config.port));

    let app = connect::router();
    let listener = tokio::net::TcpListener::bind(addr).await?;
    info!(%addr, version = differ::diff::DIFFER_VERSION, "differ listening");

    axum::serve(listener, app)
        .with_graceful_shutdown(async {
            let _ = tokio::signal::ctrl_c().await;
            info!("received SIGINT, shutting down");
        })
        .await?;

    Ok(())
}

/// Connects to 127.0.0.1:$DIFFER_PORT/healthz with a minimal hand-rolled HTTP/1.1
/// request (no HTTP client dependency needed for one GET) and returns the process
/// exit code: 0 on a 200 response within the timeout, 1 otherwise.
async fn healthcheck() -> i32 {
    let port = Config::from_env().port;
    match tokio::time::timeout(Duration::from_secs(2), probe(port)).await {
        Ok(Ok(true)) => 0,
        Ok(Ok(false)) => 1,
        Ok(Err(_)) | Err(_) => 1,
    }
}

async fn probe(port: u16) -> std::io::Result<bool> {
    let mut stream = TcpStream::connect((Ipv4Addr::LOCALHOST, port)).await?;
    stream
        .write_all(b"GET /healthz HTTP/1.1\r\nHost: 127.0.0.1\r\nConnection: close\r\n\r\n")
        .await?;
    let mut buf = [0u8; 32];
    let n = stream.read(&mut buf).await?;
    Ok(buf[..n].starts_with(b"HTTP/1.1 200"))
}
