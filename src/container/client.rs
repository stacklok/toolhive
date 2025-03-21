use async_trait::async_trait;
use bollard::{
    container::{
        AttachContainerOptions, Config, CreateContainerOptions, InspectContainerOptions,
        ListContainersOptions, LogsOptions, RemoveContainerOptions, StartContainerOptions,
        StopContainerOptions,
    },
    models::{HostConfig, Mount, MountTypeEnum},
    Docker,
};
use futures::stream::Stream;
use futures::stream::StreamExt;
use std::collections::HashMap;
use std::env;
use std::path::{Path, PathBuf};
use std::pin::Pin;
use std::task::{Context, Poll};
use std::time::{SystemTime, UNIX_EPOCH};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};

use crate::container::{ContainerInfo, ContainerRuntime, PortMapping};
use crate::error::{Error, Result};
use crate::labels;
use crate::permissions::profile::ContainerPermissionConfig;

// Container socket paths
// Podman socket paths
const PODMAN_SOCKET_PATH: &str = "/var/run/podman/podman.sock";
const PODMAN_XDG_RUNTIME_SOCKET_PATH: &str = "podman/podman.sock";
// Docker socket paths
const DOCKER_SOCKET_PATH: &str = "/var/run/docker.sock";

/// Runtime type
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum RuntimeType {
    Podman,
    Docker,
}

/// Client for interacting with container runtimes (Podman or Docker)
pub struct ContainerClient {
    client: Docker,
    pub runtime_type: RuntimeType,
}

impl ContainerClient {
    /// Create a new container client
    pub async fn new() -> Result<Self> {
        // Try to find a container socket in various locations
        let (socket_path, runtime_type) = Self::find_container_socket()?;
        Self::with_socket_path(&socket_path, runtime_type).await
    }

    /// Parse a timestamp string into a Unix timestamp (seconds since epoch)
    fn parse_timestamp(timestamp: &str) -> u64 {
        // Try to parse the timestamp using chrono
        if let Ok(dt) = chrono::DateTime::parse_from_rfc3339(timestamp) {
            return dt.timestamp() as u64;
        }

        // Fallback to current time if parsing fails
        match SystemTime::now().duration_since(UNIX_EPOCH) {
            Ok(duration) => duration.as_secs(),
            Err(_) => 0,
        }
    }

    /// Create a new container client with a custom socket path
    pub async fn with_socket_path(socket_path: &str, runtime_type: RuntimeType) -> Result<Self> {
        let runtime_name = match runtime_type {
            RuntimeType::Podman => "Podman",
            RuntimeType::Docker => "Docker",
        };

        log::debug!(
            "Creating {} client with socket path: {}",
            runtime_name,
            socket_path
        );

        // Check if the socket exists
        if !Path::new(socket_path).exists() {
            return Err(Error::ContainerRuntime(format!(
                "{} socket not found at {}",
                runtime_name, socket_path
            )));
        }

        // Create Docker client with Unix socket support
        // Use the bollard library to connect to the container socket
        let client = match Docker::connect_with_socket(
            socket_path,
            120,                          // timeout in seconds
            bollard::API_DEFAULT_VERSION, // API version
        ) {
            Ok(client) => client,
            Err(e) => {
                return Err(Error::ContainerRuntime(format!(
                    "Failed to connect to {} socket: {}",
                    runtime_name, e
                )));
            }
        };

        let container_client = Self {
            client,
            runtime_type,
        };

        // Verify that the container runtime is available by pinging the API
        match container_client.client.ping().await {
            Ok(_) => {
                log::debug!("{} client created successfully", runtime_name);
                Ok(container_client)
            }
            Err(e) => Err(Error::ContainerRuntime(format!(
                "{} API ping failed: {}",
                runtime_name, e
            ))),
        }
    }

    /// Find a container socket path, preferring Podman over Docker
    fn find_container_socket() -> Result<(String, RuntimeType)> {
        log::debug!("Searching for container socket...");

        // Try Podman sockets first

        // Check standard Podman location
        if Path::new(PODMAN_SOCKET_PATH).exists() {
            log::debug!("Found Podman socket at: {}", PODMAN_SOCKET_PATH);
            return Ok((PODMAN_SOCKET_PATH.to_string(), RuntimeType::Podman));
        }

        // Check XDG_RUNTIME_DIR location for Podman
        if let Ok(xdg_runtime_dir) = env::var("XDG_RUNTIME_DIR") {
            let xdg_socket_path =
                PathBuf::from(xdg_runtime_dir).join(PODMAN_XDG_RUNTIME_SOCKET_PATH);

            if xdg_socket_path.exists() {
                log::debug!("Found Podman socket at: {}", xdg_socket_path.display());
                return Ok((
                    xdg_socket_path.to_string_lossy().to_string(),
                    RuntimeType::Podman,
                ));
            }
        }

        // Check user-specific location for Podman
        if let Ok(home) = env::var("HOME") {
            let user_socket_path =
                PathBuf::from(home).join(".local/share/containers/podman/machine/podman.sock");

            if user_socket_path.exists() {
                log::debug!("Found Podman socket at: {}", user_socket_path.display());
                return Ok((
                    user_socket_path.to_string_lossy().to_string(),
                    RuntimeType::Podman,
                ));
            }
        }

        // Try Docker socket as fallback
        if Path::new(DOCKER_SOCKET_PATH).exists() {
            log::debug!("Found Docker socket at: {}", DOCKER_SOCKET_PATH);
            return Ok((DOCKER_SOCKET_PATH.to_string(), RuntimeType::Docker));
        }

        Err(Error::ContainerRuntime(
            "No container socket found".to_string(),
        ))
    }
}

/// Container attach stream for reading
pub struct ContainerAttachReader {
    output: Pin<
        Box<
            dyn Stream<
                    Item = std::result::Result<
                        bollard::container::LogOutput,
                        bollard::errors::Error,
                    >,
                > + Send,
        >,
    >,
    buffer: Vec<u8>,
}

/// Container attach stream for writing
pub struct ContainerAttachWriter {
    input: Pin<Box<dyn AsyncWrite + Send>>,
}

impl AsyncRead for ContainerAttachReader {
    fn poll_read(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &mut ReadBuf<'_>,
    ) -> Poll<std::io::Result<()>> {
        // If we have data in the buffer, use that first
        if !self.buffer.is_empty() {
            let len = std::cmp::min(buf.remaining(), self.buffer.len());
            buf.put_slice(&self.buffer[..len]);
            self.buffer = self.buffer[len..].to_vec();
            return Poll::Ready(Ok(()));
        }

        // Poll the reader for data
        match self.output.as_mut().poll_next(cx) {
            Poll::Ready(Some(Ok(log_output))) => {
                // Extract the message bytes
                let bytes = match log_output {
                    bollard::container::LogOutput::StdOut { message } => message,
                    bollard::container::LogOutput::StdErr { message } => message,
                    _ => return Poll::Ready(Ok(())), // Skip other message types
                };

                // Copy the bytes into the buffer
                let len = std::cmp::min(buf.remaining(), bytes.len());
                buf.put_slice(&bytes[..len]);

                // Store any remaining data in the buffer
                if len < bytes.len() {
                    self.buffer = bytes[len..].to_vec();
                }

                Poll::Ready(Ok(()))
            }
            Poll::Ready(Some(Err(e))) => {
                Poll::Ready(Err(std::io::Error::new(std::io::ErrorKind::Other, e)))
            }
            Poll::Ready(None) => {
                // End of stream
                Poll::Ready(Ok(()))
            }
            Poll::Pending => Poll::Pending,
        }
    }
}

impl AsyncWrite for ContainerAttachWriter {
    fn poll_write(
        mut self: Pin<&mut Self>,
        cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<std::io::Result<usize>> {
        self.input.as_mut().poll_write(cx, buf)
    }

    fn poll_flush(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<std::io::Result<()>> {
        self.input.as_mut().poll_flush(cx)
    }

    fn poll_shutdown(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<std::io::Result<()>> {
        self.input.as_mut().poll_shutdown(cx)
    }
}

#[async_trait]
impl ContainerRuntime for ContainerClient {
    async fn create_container(
        &self,
        image: &str,
        name: &str,
        command: Vec<String>,
        env_vars: HashMap<String, String>,
        labels: HashMap<String, String>,
        permission_config: ContainerPermissionConfig,
    ) -> Result<String> {
        // Convert mounts to the format Podman expects
        let mounts: Vec<Mount> = permission_config
            .mounts
            .iter()
            .map(|mount| Mount {
                target: Some(mount.target.clone()),
                source: Some(mount.source.clone()),
                typ: Some(MountTypeEnum::BIND),
                read_only: Some(mount.read_only),
                ..Default::default()
            })
            .collect();

        // Convert environment variables to Vec<String>
        let env: Vec<String> = env_vars
            .iter()
            .map(|(key, value)| format!("{}={}", key, value))
            .collect();

        // Check if STDIO transport is being used
        let is_stdio_transport = env_vars.get("MCP_TRANSPORT").is_some_and(|v| v == "stdio");

        // Create container configuration
        let config = Config {
            image: Some(image.to_string()),
            cmd: Some(command),
            env: Some(env),
            labels: Some(labels),
            attach_stdin: Some(is_stdio_transport),
            attach_stdout: Some(is_stdio_transport),
            attach_stderr: Some(is_stdio_transport),
            open_stdin: Some(is_stdio_transport),
            host_config: Some(HostConfig {
                mounts: Some(mounts),
                network_mode: Some(permission_config.network_mode),
                cap_drop: Some(permission_config.cap_drop),
                cap_add: Some(permission_config.cap_add),
                security_opt: Some(permission_config.security_opt),
                ..Default::default()
            }),
            ..Default::default()
        };

        // Create container
        let options = CreateContainerOptions {
            name,
            platform: None,
        };

        match self.client.create_container(Some(options), config).await {
            Ok(response) => {
                // Log warnings if any
                for warning in &response.warnings {
                    log::debug!("Container create warning: {}", warning);
                }
                Ok(response.id)
            }
            Err(e) => Err(Error::ContainerRuntime(format!(
                "Failed to create container: {}",
                e
            ))),
        }
    }

    async fn start_container(&self, container_id: &str) -> Result<()> {
        // Start container
        match self
            .client
            .start_container(container_id, None::<StartContainerOptions<String>>)
            .await
        {
            Ok(_) => Ok(()),
            Err(e) => Err(Error::ContainerRuntime(format!(
                "Failed to start container: {}",
                e
            ))),
        }
    }

    async fn list_containers(&self) -> Result<Vec<ContainerInfo>> {
        // Create filter for vibetool containers
        let mut filters = HashMap::new();
        filters.insert("label".to_string(), vec![labels::format_vibetool_filter()]);

        let options = ListContainersOptions {
            all: true,
            filters,
            ..Default::default()
        };

        // List containers
        match self.client.list_containers(Some(options)).await {
            Ok(containers) => {
                // Convert to our ContainerInfo format
                let container_infos = containers
                    .into_iter()
                    .map(|c| {
                        // Extract port mappings
                        let ports = c
                            .ports
                            .unwrap_or_default()
                            .into_iter()
                            .map(|port| {
                                // Get the container port
                                let container_port = port.private_port;

                                // Get the host port
                                let host_port = port.public_port.unwrap_or(0);

                                // Get the protocol
                                let protocol = match port.typ {
                                    Some(port_type) => port_type.to_string(),
                                    None => "tcp".to_string(),
                                };

                                PortMapping {
                                    container_port,
                                    host_port,
                                    protocol,
                                }
                            })
                            .collect();

                        // Extract container name (remove leading slash)
                        let name = c
                            .names
                            .unwrap_or_default()
                            .first()
                            .cloned()
                            .unwrap_or_default()
                            .trim_start_matches('/')
                            .to_string();

                        ContainerInfo {
                            id: c.id.unwrap_or_default(),
                            name,
                            image: c.image.unwrap_or_default(),
                            status: c.status.unwrap_or_default(),
                            state: c.state.unwrap_or_default(),
                            created: c.created.unwrap_or(0) as u64,
                            labels: c.labels.unwrap_or_default(),
                            ports,
                        }
                    })
                    .collect();

                Ok(container_infos)
            }
            Err(e) => Err(Error::ContainerRuntime(format!(
                "Failed to list containers: {}",
                e
            ))),
        }
    }

    async fn stop_container(&self, container_id: &str) -> Result<()> {
        // Stop container
        match self
            .client
            .stop_container(container_id, None::<StopContainerOptions>)
            .await
        {
            Ok(_) => Ok(()),
            Err(e) => Err(Error::ContainerRuntime(format!(
                "Failed to stop container: {}",
                e
            ))),
        }
    }

    async fn remove_container(&self, container_id: &str) -> Result<()> {
        // Remove container
        let options = RemoveContainerOptions {
            force: true,
            ..Default::default()
        };

        match self
            .client
            .remove_container(container_id, Some(options))
            .await
        {
            Ok(_) => Ok(()),
            Err(e) => Err(Error::ContainerRuntime(format!(
                "Failed to remove container: {}",
                e
            ))),
        }
    }

    async fn container_logs(&self, container_id: &str) -> Result<String> {
        // Get container logs
        let options = LogsOptions::<String> {
            stdout: true,
            stderr: true,
            ..Default::default()
        };

        // Get logs stream
        let logs_stream = self.client.logs(container_id, Some(options));

        // Collect all log messages
        let mut logs = String::new();
        let mut stream = logs_stream;

        while let Some(log_result) = stream.next().await {
            match log_result {
                Ok(log_output) => match log_output {
                    bollard::container::LogOutput::StdOut { message } => {
                        logs.push_str(&String::from_utf8_lossy(&message));
                    }
                    bollard::container::LogOutput::StdErr { message } => {
                        logs.push_str(&String::from_utf8_lossy(&message));
                    }
                    _ => {}
                },
                Err(e) => {
                    return Err(Error::ContainerRuntime(format!(
                        "Error reading container logs: {}",
                        e
                    )));
                }
            }
        }

        Ok(logs)
    }

    async fn is_container_running(&self, container_id: &str) -> Result<bool> {
        // Get container info
        match self
            .client
            .inspect_container(container_id, None::<InspectContainerOptions>)
            .await
        {
            Ok(container) => {
                // Check if the container is running
                Ok(container.state.and_then(|s| s.running).unwrap_or(false))
            }
            Err(bollard::errors::Error::DockerResponseServerError {
                status_code: 404, ..
            }) => {
                // Container not found
                Err(Error::ContainerNotFound(container_id.to_string()))
            }
            Err(e) => Err(Error::ContainerRuntime(format!(
                "Failed to check if container is running: {}",
                e
            ))),
        }
    }

    async fn get_container_ip(&self, container_id: &str) -> Result<String> {
        // Get container info
        match self
            .client
            .inspect_container(container_id, None::<InspectContainerOptions>)
            .await
        {
            Ok(container) => {
                // Get the container's IP address from the network settings
                if let Some(network_settings) = container.network_settings {
                    // First try to get the IP from the default bridge network
                    if let Some(networks) = network_settings.networks {
                        if let Some(bridge) = networks.get("bridge") {
                            if let Some(ip) = &bridge.ip_address {
                                if !ip.is_empty() {
                                    return Ok(ip.clone());
                                }
                            }
                        }

                        // If bridge network doesn't have an IP, try any other network
                        for (_, network) in networks {
                            if let Some(ip) = network.ip_address {
                                if !ip.is_empty() {
                                    return Ok(ip);
                                }
                            }
                        }
                    }
                }

                // If we couldn't find an IP address, return an error
                Err(Error::ContainerRuntime(format!(
                    "No IP address found for container {}",
                    container_id
                )))
            }
            Err(bollard::errors::Error::DockerResponseServerError {
                status_code: 404, ..
            }) => {
                // Container not found
                Err(Error::ContainerNotFound(container_id.to_string()))
            }
            Err(e) => Err(Error::ContainerRuntime(format!(
                "Failed to get container IP: {}",
                e
            ))),
        }
    }

    async fn get_container_info(&self, container_id: &str) -> Result<ContainerInfo> {
        // Get container info
        match self
            .client
            .inspect_container(container_id, None::<InspectContainerOptions>)
            .await
        {
            Ok(container) => {
                // Extract port mappings
                let mut ports = Vec::new();
                if let Some(network_settings) = &container.network_settings {
                    if let Some(port_map) = &network_settings.ports {
                        for (port_proto, bindings) in port_map {
                            if let Some(bindings) = bindings {
                                for binding in bindings {
                                    if let Some(host_port_str) = &binding.host_port {
                                        if let Ok(host_port) = host_port_str.parse::<u16>() {
                                            // Parse container port and protocol from the key (e.g., "80/tcp")
                                            let parts: Vec<&str> = port_proto.split('/').collect();
                                            if parts.len() == 2 {
                                                if let Ok(container_port) = parts[0].parse::<u16>()
                                                {
                                                    ports.push(PortMapping {
                                                        container_port,
                                                        host_port,
                                                        protocol: parts[1].to_string(),
                                                    });
                                                }
                                            }
                                        }
                                    }
                                }
                            }
                        }
                    }
                }

                // Extract container name (remove leading slash)
                let name = container
                    .name
                    .unwrap_or_default()
                    .trim_start_matches('/')
                    .to_string();

                // Extract container state
                let (status, state) = if let Some(container_state) = &container.state {
                    let status_str = if let Some(status) = &container_state.status {
                        status.to_string()
                    } else {
                        "unknown".to_string()
                    };
                    (status_str.clone(), status_str)
                } else {
                    ("unknown".to_string(), "unknown".to_string())
                };

                // Extract created timestamp
                let created_timestamp = if let Some(created_str) = container.created {
                    Self::parse_timestamp(&created_str)
                } else {
                    0 // Default to 0 if no timestamp is available
                };

                // Extract labels and image
                let (labels, image) = if let Some(ref config) = container.config {
                    (
                        config.labels.clone().unwrap_or_default(),
                        config.image.clone().unwrap_or_default(),
                    )
                } else {
                    (HashMap::new(), "".to_string())
                };

                Ok(ContainerInfo {
                    id: container.id.unwrap_or_default(),
                    name,
                    image,
                    status,
                    state,
                    created: created_timestamp,
                    labels,
                    ports,
                })
            }
            Err(bollard::errors::Error::DockerResponseServerError {
                status_code: 404, ..
            }) => {
                // Container not found
                Err(Error::ContainerNotFound(container_id.to_string()))
            }
            Err(e) => Err(Error::ContainerRuntime(format!(
                "Failed to get container info: {}",
                e
            ))),
        }
    }

    async fn attach_container(
        &self,
        container_id: &str,
    ) -> Result<(
        Box<dyn AsyncWrite + Unpin + Send>,
        Box<dyn AsyncRead + Unpin + Send>,
    )> {
        log::debug!("Attaching to container {}", container_id);

        // Create attach options
        let options = AttachContainerOptions::<String> {
            stdin: Some(true),
            stdout: Some(true),
            stderr: Some(true),
            stream: Some(true),
            ..Default::default()
        };

        // Attach to the container
        match self
            .client
            .attach_container(container_id, Some(options))
            .await
        {
            Ok(results) => {
                // Create reader and writer
                let reader = ContainerAttachReader {
                    output: results.output,
                    buffer: Vec::new(),
                };

                let writer = ContainerAttachWriter {
                    input: results.input,
                };

                // Return the reader and writer
                let reader_box: Box<dyn AsyncRead + Unpin + Send> = Box::new(reader);
                let writer_box: Box<dyn AsyncWrite + Unpin + Send> = Box::new(writer);

                Ok((writer_box, reader_box))
            }
            Err(e) => Err(Error::ContainerRuntime(format!(
                "Failed to attach to container: {}",
                e
            ))),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mockall::predicate::*;
    use mockall::*;

    // Mock for testing
    mock! {
        pub ContainerClient {}

        #[async_trait]
        impl ContainerRuntime for ContainerClient {
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

    // Tests would go here, but they would require mocking the Docker client
    // which is beyond the scope of this implementation
}
