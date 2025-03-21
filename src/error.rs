use thiserror::Error;

#[derive(Error, Debug)]
pub enum Error {
    #[error("IO error: {0}")]
    Io(#[from] std::io::Error),

    #[error("HTTP error: {0}")]
    Http(#[from] http::Error),

    #[error("Hyper error: {0}")]
    Hyper(#[from] hyper::Error),

    #[error("JSON error: {0}")]
    Json(#[from] serde_json::Error),

    #[error("Request error: {0}")]
    Request(#[from] reqwest::Error),

    #[error("URL parse error: {0}")]
    UrlParse(#[from] url::ParseError),

    #[error("Container runtime error: {0}")]
    ContainerRuntime(String),

    #[error("Permission error: {0}")]
    Permission(String),

    #[error("Transport error: {0}")]
    Transport(String),

    #[error("Proxy error: {0}")]
    Proxy(String),

    #[error("Container not found: {0}")]
    ContainerNotFound(String),

    #[error("Container exited unexpectedly: {0}")]
    ContainerExited(String),

    #[error("Invalid argument: {0}")]
    InvalidArgument(String),

    #[error("Configuration error: {0}")]
    Configuration(String),
}

pub type Result<T> = std::result::Result<T, Error>;
