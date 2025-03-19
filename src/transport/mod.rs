use async_trait::async_trait;
use std::any::Any;
use std::collections::HashMap;
use std::fmt::Debug;

use crate::error::Result;

pub mod sse;
pub mod stdio;

/// Transport mode for MCP servers
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TransportMode {
    /// Server-Sent Events (SSE) transport
    SSE,
    /// Standard I/O transport
    STDIO,
}

impl TransportMode {
    /// Convert a string to a transport mode
    pub fn from_str(s: &str) -> Option<Self> {
        match s.to_lowercase().as_str() {
            "sse" => Some(Self::SSE),
            "stdio" => Some(Self::STDIO),
            _ => None,
        }
    }

    /// Convert a transport mode to a string
    pub fn as_str(&self) -> &'static str {
        match self {
            Self::SSE => "sse",
            Self::STDIO => "stdio",
        }
    }
}

/// Common trait for transport handlers
#[async_trait]
pub trait Transport: Send + Sync {
    /// Get the transport mode
    fn mode(&self) -> TransportMode;

    /// Get the port used by the transport
    fn port(&self) -> u16;

    /// Set up the transport
    async fn setup(
        &self,
        container_id: &str,
        container_name: &str,
        env_vars: &mut HashMap<String, String>,
        container_ip: Option<String>,
    ) -> Result<()>;

    /// Start the transport
    ///
    /// For STDIO transport, stdin and stdout are provided from the container's attach
    async fn start(
        &self,
        stdin: Option<Box<dyn tokio::io::AsyncWrite + Unpin + Send>>,
        stdout: Option<Box<dyn tokio::io::AsyncRead + Unpin + Send>>,
    ) -> Result<()>;

    /// Stop the transport
    async fn stop(&self) -> Result<()>;

    /// Check if the transport is running
    async fn is_running(&self) -> Result<bool>;
    
    /// Convert to Any for downcasting
    fn as_any(&self) -> &dyn Any;
}

/// Factory for creating transport handlers
pub struct TransportFactory;

impl TransportFactory {
    /// Create a transport handler based on the mode
    pub fn create(mode: TransportMode, port: u16) -> Box<dyn Transport> {
        match mode {
            TransportMode::SSE => Box::new(sse::SseTransport::new(port)),
            TransportMode::STDIO => Box::new(stdio::StdioTransport::new(port)),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::transport::sse::SseTransport;
    use crate::transport::stdio::StdioTransport;

    #[test]
    fn test_transport_mode_from_str() {
        assert_eq!(TransportMode::from_str("sse"), Some(TransportMode::SSE));
        assert_eq!(TransportMode::from_str("SSE"), Some(TransportMode::SSE));
        assert_eq!(TransportMode::from_str("stdio"), Some(TransportMode::STDIO));
        assert_eq!(TransportMode::from_str("STDIO"), Some(TransportMode::STDIO));
        assert_eq!(TransportMode::from_str("invalid"), None);
    }

    #[test]
    fn test_transport_mode_as_str() {
        assert_eq!(TransportMode::SSE.as_str(), "sse");
        assert_eq!(TransportMode::STDIO.as_str(), "stdio");
    }

    #[test]
    fn test_transport_factory_create() {
        // Test creating SSE transport
        let sse_transport = TransportFactory::create(TransportMode::SSE, 8080);
        assert_eq!(sse_transport.mode(), TransportMode::SSE);
        
        // Test creating STDIO transport
        let stdio_transport = TransportFactory::create(TransportMode::STDIO, 8081);
        assert_eq!(stdio_transport.mode(), TransportMode::STDIO);
        
        // Verify the correct types were created using downcasting
        assert!(sse_transport.as_any().downcast_ref::<SseTransport>().is_some());
        assert!(stdio_transport.as_any().downcast_ref::<StdioTransport>().is_some());
    }
}