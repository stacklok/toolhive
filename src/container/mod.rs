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
    
    // For testing only
    #[cfg(test)]
    pub async fn create_with_runtime(runtime: Box<dyn ContainerRuntime>) -> Result<Box<dyn ContainerRuntime>> {
        Ok(runtime)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mockall::predicate::*;
    use mockall::*;
    
    // Mock for testing
    mock! {
        pub ContainerRuntime {}
        
        #[async_trait]
        impl ContainerRuntime for ContainerRuntime {
            async fn create_and_start_container(
                &self,
                image: &str,
                name: &str,
                command: Vec<String>,
                env_vars: HashMap<String, String>,
                labels: HashMap<String, String>,
                permission_config: ContainerPermissionConfig,
            ) -> Result<String>;
            
            async fn list_containers(&self) -> Result<Vec<ContainerInfo>>;
            async fn stop_container(&self, container_id: &str) -> Result<()>;
            async fn remove_container(&self, container_id: &str) -> Result<()>;
            async fn container_logs(&self, container_id: &str) -> Result<String>;
            async fn is_container_running(&self, container_id: &str) -> Result<bool>;
            async fn get_container_info(&self, container_id: &str) -> Result<ContainerInfo>;
            async fn attach_container(&self, container_id: &str) -> Result<(Box<dyn AsyncWrite + Unpin + Send>, Box<dyn AsyncRead + Unpin + Send>)>;
        }
    }
    
    #[tokio::test]
    async fn test_container_runtime_factory_create_with_runtime() -> Result<()> {
        // Create a mock runtime
        let mut mock_runtime = MockContainerRuntime::new();
        
        // Set up expectations for list_containers
        mock_runtime.expect_list_containers()
            .times(1)
            .returning(|| {
                Ok(vec![
                    ContainerInfo {
                        id: "container1".to_string(),
                        name: "test-container-1".to_string(),
                        image: "test-image".to_string(),
                        status: "Up 10 minutes".to_string(),
                        created: 0,
                        labels: HashMap::new(),
                        ports: vec![],
                    },
                ])
            });
        
        // Create a runtime using the factory
        let runtime = ContainerRuntimeFactory::create_with_runtime(Box::new(mock_runtime)).await?;
        
        // Test the runtime
        let containers = runtime.list_containers().await?;
        
        // Verify the result
        assert_eq!(containers.len(), 1);
        assert_eq!(containers[0].id, "container1");
        assert_eq!(containers[0].name, "test-container-1");
        
        Ok(())
    }
}