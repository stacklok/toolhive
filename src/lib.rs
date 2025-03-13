//! mcp-lok is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers.
//!
//! It acts as a thin client for the Docker/Podman Unix socket API, providing
//! container-based isolation for running MCP servers with minimal permissions.

pub mod cli;
pub mod container;
pub mod permissions;

// Re-export the container module and TransportMode enum for tests
pub use container::ContainerManager;
pub use container::TransportMode;