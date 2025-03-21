pub mod list;
pub mod rm;
pub mod run;
pub mod stop;

#[cfg(test)]
mod tests {
    use async_trait::async_trait;
    use mockall::predicate::*;
    use mockall::*;
    use std::collections::HashMap;
    use tokio::io::{AsyncRead, AsyncWrite};

    use crate::container::{ContainerInfo, ContainerRuntime, PortMapping};
    use crate::error::Result;
    use crate::permissions::profile::ContainerPermissionConfig;

    // Mock for testing
    mock! {
        pub ContainerRuntime {}

        #[async_trait]
        impl ContainerRuntime for ContainerRuntime {
            async fn create_container(
                &self,
                image: &str,
                name: &str,
                command: Vec<String>,
                env_vars: HashMap<String, String>,
                labels: HashMap<String, String>,
                permission_config: ContainerPermissionConfig,
            ) -> Result<String>;

            async fn start_container(&self, container_id: &str) -> Result<()>;

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
        // Derive state from status string
        let state = if status.starts_with("Up") {
            "running".to_string()
        } else if status.starts_with("Exited") || status.starts_with("Dead") {
            "exited".to_string()
        } else if status.starts_with("Created") {
            "created".to_string()
        } else if status.starts_with("Restarting") {
            "restarting".to_string()
        } else if status.starts_with("Paused") {
            "paused".to_string()
        } else {
            "unknown".to_string()
        };

        ContainerInfo {
            id: id.to_string(),
            name: name.to_string(),
            image: "test-image".to_string(),
            status: status.to_string(),
            state,
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
