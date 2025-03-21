use async_trait::async_trait;
use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;
use tokio::io::{AsyncRead, AsyncWrite};
use tokio::sync::{mpsc, Mutex};
use tokio::task::JoinHandle;

use crate::error::{Error, Result};
use crate::permissions::profile::ContainerPermissionConfig;

pub mod client;

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
    /// Container state
    pub state: String,
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
    /// Create a container without starting it
    async fn create_container(
        &self,
        image: &str,
        name: &str,
        command: Vec<String>,
        env_vars: HashMap<String, String>,
        labels: HashMap<String, String>,
        permission_config: ContainerPermissionConfig,
    ) -> Result<String>;

    /// Start a container
    async fn start_container(&self, container_id: &str) -> Result<()>;

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

    /// Get container IP address
    async fn get_container_ip(&self, container_id: &str) -> Result<String>;

    /// Attach to a container
    async fn attach_container(
        &self,
        container_id: &str,
    ) -> Result<(
        Box<dyn AsyncWrite + Unpin + Send>,
        Box<dyn AsyncRead + Unpin + Send>,
    )>;
}

/// Container monitor for watching container state
pub struct ContainerMonitor {
    runtime: Arc<Mutex<Box<dyn ContainerRuntime>>>,
    container_id: String,
    container_name: String,
    monitor_handle: Option<JoinHandle<()>>,
    exit_tx: Option<mpsc::Sender<()>>,
}

impl ContainerMonitor {
    /// Create a new container monitor
    pub fn new(
        runtime: Box<dyn ContainerRuntime>,
        container_id: &str,
        container_name: &str,
    ) -> Self {
        Self {
            runtime: Arc::new(Mutex::new(runtime)),
            container_id: container_id.to_string(),
            container_name: container_name.to_string(),
            monitor_handle: None,
            exit_tx: None,
        }
    }

    /// Start monitoring the container
    pub async fn start_monitoring(&mut self) -> Result<mpsc::Receiver<Error>> {
        // Create channels for communication
        let (exit_tx, mut exit_rx) = mpsc::channel::<()>(1);
        let (error_tx, error_rx) = mpsc::channel::<Error>(1);

        self.exit_tx = Some(exit_tx);

        // Clone values for the monitoring task
        let runtime = self.runtime.clone();
        let container_id = self.container_id.clone();
        let container_name = self.container_name.clone();
        let error_tx_clone = error_tx.clone();

        // Start the monitoring task
        let handle = tokio::spawn(async move {
            let check_interval = Duration::from_secs(5);

            loop {
                // Check if we should exit
                if exit_rx.try_recv().is_ok() {
                    break;
                }

                // Check if the container is still running
                let is_running = match runtime
                    .lock()
                    .await
                    .is_container_running(&container_id)
                    .await
                {
                    Ok(running) => running,
                    Err(e) => {
                        // If we can't check the container status, assume it's not running
                        if let Error::ContainerNotFound(_) = e {
                            let exit_error = Error::ContainerExited(format!(
                                "Container {} ({}) not found, it may have been removed",
                                container_name, container_id
                            ));
                            let _ = error_tx_clone.send(exit_error).await;
                        } else {
                            // For other errors, just log and continue
                            eprintln!("Error checking container status: {}", e);
                        }
                        tokio::time::sleep(check_interval).await;
                        continue;
                    }
                };

                if !is_running {
                    // Get container logs to help diagnose the issue
                    let logs = match runtime.lock().await.container_logs(&container_id).await {
                        Ok(logs) => logs,
                        Err(_) => "Could not retrieve container logs".to_string(),
                    };

                    // Get container info to check exit code
                    let exit_info =
                        match runtime.lock().await.get_container_info(&container_id).await {
                            Ok(info) => format!("status: {}", info.status),
                            Err(_) => "Could not retrieve container exit information".to_string(),
                        };

                    // Send error notification
                    let exit_error = Error::ContainerExited(format!(
                        "Container {} ({}) exited unexpectedly. Exit info: {}. Last logs:\n{}",
                        container_name, container_id, exit_info, logs
                    ));

                    let _ = error_tx_clone.send(exit_error).await;
                    break;
                }

                // Wait before checking again
                tokio::time::sleep(check_interval).await;
            }
        });

        self.monitor_handle = Some(handle);

        Ok(error_rx)
    }

    /// Stop monitoring the container
    pub async fn stop_monitoring(&mut self) {
        if let Some(exit_tx) = &self.exit_tx {
            let _ = exit_tx.send(()).await;
        }

        if let Some(handle) = self.monitor_handle.take() {
            let _ = handle.await;
        }

        self.exit_tx = None;
    }
}

/// Factory for creating container runtimes
pub struct ContainerRuntimeFactory;

impl ContainerRuntimeFactory {
    /// Create a container runtime
    pub async fn create() -> Result<Box<dyn ContainerRuntime>> {
        // Create a container client that supports both Podman and Docker
        let client = client::ContainerClient::new().await?;
        Ok(Box::new(client))
    }

    // For testing only
    #[cfg(test)]
    pub async fn create_with_runtime(
        runtime: Box<dyn ContainerRuntime>,
    ) -> Result<Box<dyn ContainerRuntime>> {
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

    #[tokio::test]
    async fn test_container_runtime_factory_create_with_runtime() -> Result<()> {
        // Create a mock runtime
        let mut mock_runtime = MockContainerRuntime::new();

        // Set up expectations for list_containers
        mock_runtime
            .expect_list_containers()
            .times(1)
            .returning(|| {
                Ok(vec![ContainerInfo {
                    id: "container1".to_string(),
                    name: "test-container-1".to_string(),
                    image: "test-image".to_string(),
                    status: "Up 10 minutes".to_string(),
                    state: "running".to_string(),
                    created: 0,
                    labels: HashMap::new(),
                    ports: vec![],
                }])
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

    #[tokio::test]
    async fn test_container_monitor_detects_exit() -> Result<()> {
        // Create a mock runtime
        let mut mock_runtime = MockContainerRuntime::new();

        // Set up expectations for is_container_running
        // First call returns true (container is running)
        // Second call returns false (container has exited)
        mock_runtime
            .expect_is_container_running()
            .with(eq("container1"))
            .times(2)
            .returning(|_| {
                static mut CALL_COUNT: u32 = 0;
                unsafe {
                    CALL_COUNT += 1;
                    if CALL_COUNT == 1 {
                        Ok(true)
                    } else {
                        Ok(false)
                    }
                }
            });

        // Set up expectations for container_logs
        mock_runtime
            .expect_container_logs()
            .with(eq("container1"))
            .times(1)
            .returning(|_| Ok("Container exited with code 1".to_string()));

        // Set up expectations for get_container_info
        mock_runtime
            .expect_get_container_info()
            .with(eq("container1"))
            .times(1)
            .returning(|_| {
                Ok(ContainerInfo {
                    id: "container1".to_string(),
                    name: "test-container".to_string(),
                    image: "test-image".to_string(),
                    status: "Exited (1) 5 seconds ago".to_string(),
                    state: "exited".to_string(),
                    created: 0,
                    labels: HashMap::new(),
                    ports: vec![],
                })
            });

        // Create a container monitor with a custom check interval for testing
        let mut monitor =
            ContainerMonitor::new(Box::new(mock_runtime), "container1", "test-container");

        // Override the check interval for testing
        // This is a bit of a hack, but we need to modify the monitor's behavior for testing
        let runtime = Arc::clone(&monitor.runtime);
        let container_id = monitor.container_id.clone();
        let container_name = monitor.container_name.clone();

        // Create channels for communication
        let (exit_tx, mut exit_rx) = mpsc::channel::<()>(1);
        let (error_tx, mut error_rx) = mpsc::channel::<Error>(1);

        monitor.exit_tx = Some(exit_tx);

        // Start a custom monitoring task with a shorter interval
        let error_tx_clone = error_tx.clone();
        let handle = tokio::spawn(async move {
            // Use a much shorter check interval for testing
            let check_interval = Duration::from_millis(10);

            loop {
                // Check if we should exit
                if exit_rx.try_recv().is_ok() {
                    break;
                }

                // Check if the container is still running
                let is_running = match runtime
                    .lock()
                    .await
                    .is_container_running(&container_id)
                    .await
                {
                    Ok(running) => running,
                    Err(e) => {
                        if let Error::ContainerNotFound(_) = e {
                            let exit_error = Error::ContainerExited(format!(
                                "Container {} ({}) not found, it may have been removed",
                                container_name, container_id
                            ));
                            let _ = error_tx_clone.send(exit_error).await;
                        } else {
                            eprintln!("Error checking container status: {}", e);
                        }
                        tokio::time::sleep(check_interval).await;
                        continue;
                    }
                };

                if !is_running {
                    // Get container logs to help diagnose the issue
                    let logs = match runtime.lock().await.container_logs(&container_id).await {
                        Ok(logs) => logs,
                        Err(_) => "Could not retrieve container logs".to_string(),
                    };

                    // Get container info to check exit code
                    let exit_info =
                        match runtime.lock().await.get_container_info(&container_id).await {
                            Ok(info) => format!("status: {}", info.status),
                            Err(_) => "Could not retrieve container exit information".to_string(),
                        };

                    // Send error notification
                    let exit_error = Error::ContainerExited(format!(
                        "Container {} ({}) exited unexpectedly. Exit info: {}. Last logs:\n{}",
                        container_name, container_id, exit_info, logs
                    ));

                    let _ = error_tx_clone.send(exit_error).await;
                    break;
                }

                // Wait before checking again (much shorter for testing)
                tokio::time::sleep(check_interval).await;
            }
        });

        monitor.monitor_handle = Some(handle);

        // Wait for the error notification (with timeout)
        let error = tokio::time::timeout(Duration::from_secs(2), error_rx.recv()).await;

        // Stop monitoring
        monitor.stop_monitoring().await;

        // Verify we received an error
        assert!(
            error.is_ok(),
            "Timeout waiting for container exit notification"
        );
        let error = error.unwrap();
        assert!(error.is_some(), "Expected error notification");

        // Verify it's a ContainerExited error
        if let Some(Error::ContainerExited(msg)) = error {
            assert!(
                msg.contains("exited unexpectedly"),
                "Error message should indicate unexpected exit"
            );
            assert!(
                msg.contains("container1"),
                "Error message should contain container ID"
            );
            assert!(
                msg.contains("test-container"),
                "Error message should contain container name"
            );
        } else {
            panic!("Expected ContainerExited error");
        }

        Ok(())
    }
}
