pub mod run;
pub mod list;
pub mod start;
pub mod stop;
pub mod rm;

#[cfg(test)]
mod tests {
    use std::collections::HashMap;
    use mockall::predicate::*;
    use mockall::*;
    use async_trait::async_trait;
    use tokio::io::{AsyncRead, AsyncWrite};
    
    use crate::container::{ContainerInfo, ContainerRuntime, PortMapping};
    use crate::error::Result;
    use crate::permissions::profile::ContainerPermissionConfig;
    
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
            async fn get_container_ip(&self, container_id: &str) -> Result<String>;
            async fn attach_container(&self, container_id: &str) -> Result<(Box<dyn AsyncWrite + Unpin + Send>, Box<dyn AsyncRead + Unpin + Send>)>;
        }
    }
    
    // Helper function to create a mock container runtime
    pub fn create_mock_runtime() -> MockContainerRuntime {
        MockContainerRuntime::new()
    }
    
    // Helper function to create a test container info
    pub fn create_test_container_info(id: &str, name: &str, status: &str) -> ContainerInfo {
        ContainerInfo {
            id: id.to_string(),
            name: name.to_string(),
            image: "test-image".to_string(),
            status: status.to_string(),
            created: 0,
            labels: HashMap::new(),
            ports: vec![PortMapping {
                container_port: 8080,
                host_port: 8080,
                protocol: "tcp".to_string(),
            }],
        }
    }
}