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

    /// Set up the transport
    async fn setup(
        &self,
        container_id: &str,
        container_name: &str,
        port: Option<u16>,
        env_vars: &mut HashMap<String, String>,
    ) -> Result<()>;

    /// Start the transport
    async fn start(&self) -> Result<()>;

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
            TransportMode::STDIO => Box::new(stdio::StdioTransport::new()),
        }
    }
}