use async_trait::async_trait;
use std::collections::HashMap;
use tokio::io::{AsyncRead, AsyncWrite};

use crate::error::Result;
use crate::permissions::profile::ContainerPermissionConfig;

pub mod docker;
pub mod podman;

/// Container information
#[derive(Debug, Clone)]
pub struct ContainerInfo {
    /// Container ID
    pub id: String,
    /// Container name
    pub name: String,
    /// Container image
    pub image: String,
    /// Container status
    pub status: String,
    /// Container creation timestamp
    pub created: u64,
    /// Container labels
    pub labels: HashMap<String, String>,
    /// Container port mappings
    pub ports: Vec<PortMapping>,
}

/// Port mapping
#[derive(Debug, Clone)]
pub struct PortMapping {
    /// Container port
    pub container_port: u16,
    /// Host port
    pub host_port: u16,
    /// Protocol (tcp, udp)
    pub protocol: String,
}

/// Common trait for container runtimes
#[async_trait]
pub trait ContainerRuntime: Send + Sync {
    /// Create and start a container
    async fn create_and_start_container(
        &self,
        image: &str,
        name: &str,
        command: Vec<String>,
        env_vars: HashMap<String, String>,
        labels: HashMap<String, String>,
        permission_config: ContainerPermissionConfig,
    ) -> Result<String>;

    /// List containers
    async fn list_containers(&self) -> Result<Vec<ContainerInfo>>;

    /// Stop a container
    async fn stop_container(&self, container_id: &str) -> Result<()>;

    /// Remove a container
    async fn remove_container(&self, container_id: &str) -> Result<()>;

    /// Get container logs
    async fn container_logs(&self, container_id: &str) -> Result<String>;

    /// Check if a container is running
    async fn is_container_running(&self, container_id: &str) -> Result<bool>;

    /// Get container information
    async fn get_container_info(&self, container_id: &str) -> Result<ContainerInfo>;
    
    /// Attach to a container
    async fn attach_container(&self, container_id: &str) -> Result<(Box<dyn AsyncWrite + Unpin + Send>, Box<dyn AsyncRead + Unpin + Send>)>;
}

/// Factory for creating container runtimes
pub struct ContainerRuntimeFactory;

impl ContainerRuntimeFactory {
    /// Create a container runtime
    pub async fn create() -> Result<Box<dyn ContainerRuntime>> {
        // Try to create a Podman client first
        if let Ok(podman) = podman::PodmanClient::new().await {
            return Ok(Box::new(podman));
        }

        // Fall back to Docker
        let docker = docker::DockerClient::new().await?;
        Ok(Box::new(docker))
    }
}