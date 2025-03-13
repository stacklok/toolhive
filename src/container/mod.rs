//! Container module for mcp-lok
//!
//! This module handles container operations using the Docker/Podman API.

use anyhow::{Context, Result};
use bollard::container::{
    Config, CreateContainerOptions, ListContainersOptions, RemoveContainerOptions,
    StartContainerOptions, StopContainerOptions,
};
use bollard::models::{HostConfig, Mount, MountTypeEnum, PortBinding};
use bollard::{Docker, ClientVersion};
use std::collections::HashMap;
// use std::io::Write;
use std::io::{Read, Write};
use std::net::{TcpListener, TcpStream};
// use std::sync::{Arc, Mutex}; // Removed unused imports
use std::thread;
// use tokio::io::{AsyncReadExt, AsyncWriteExt};
// use tokio::sync::oneshot;
use tracing::{debug, error, info};

use crate::permissions::PermissionProfile;

/// MCP server container manager
pub struct ContainerManager {
    docker: Docker,
}

/// Transport mode for MCP servers
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum TransportMode {
    /// Server-Sent Events transport
    SSE,
    /// Standard I/O transport
    STDIO,
}

impl TransportMode {
    /// Parse a transport mode string
    pub fn from_str(s: &str) -> Result<Self> {
        match s.to_lowercase().as_str() {
            "sse" => Ok(TransportMode::SSE),
            "stdio" => Ok(TransportMode::STDIO),
            _ => anyhow::bail!("Invalid transport mode: {}", s),
        }
    }
}

impl ContainerManager {
    /// Create a new container manager
    pub async fn new() -> Result<Self> {
        // Print debug information about the environment
        info!("Current working directory: {:?}", std::env::current_dir());
        
        // Check if Docker socket exists
        let docker_socket = std::path::Path::new("/var/run/docker.sock");
        info!("Docker socket exists: {}", docker_socket.exists());
        
        let podman_socket_1 = std::path::Path::new("/var/run/podman/podman.sock");
        info!("Podman socket 1 exists: {}", podman_socket_1.exists());
        
        // Check for Podman socket in XDG_RUNTIME_DIR
        let xdg_runtime_dir = std::env::var("XDG_RUNTIME_DIR").unwrap_or_else(|_| "/tmp".to_string());
        info!("XDG_RUNTIME_DIR: {}", xdg_runtime_dir);
        
        let podman_socket_2 = std::path::PathBuf::from(xdg_runtime_dir).join("podman/podman.sock");
        info!("Podman socket 2 path: {}", podman_socket_2.display());
        info!("Podman socket 2 exists: {}", podman_socket_2.exists());
        
        // Try to connect to Docker or Podman socket
        let docker = if docker_socket.exists() {
            info!("Attempting to connect to Docker socket at /var/run/docker.sock");
            Docker::connect_with_socket_defaults()
                .context("Failed to connect to Docker socket")?
        } else if podman_socket_1.exists() {
            info!("Attempting to connect to Podman socket at /var/run/podman/podman.sock");
            Docker::connect_with_socket_defaults()
                .context("Failed to connect to Podman socket at /var/run/podman/podman.sock")?
        } else if podman_socket_2.exists() {
            info!("Attempting to connect to Podman socket at {}", podman_socket_2.display());
            // Connect directly to the Podman socket in XDG_RUNTIME_DIR
            let socket_path = podman_socket_2.to_str().unwrap();
            info!("Connecting directly to socket at {}", socket_path);
            
            // Create a ClientVersion with the correct version values
            let client_version = ClientVersion {
                major_version: 1,
                minor_version: 47,
            };
            
            // Try to connect with the socket path directly
            Docker::connect_with_socket(socket_path, 30, &client_version)
                .context(format!("Failed to connect to Podman socket at {}", podman_socket_2.display()))?
        } else {
            // List all files in /var/run to help diagnose the issue
            if let Ok(entries) = std::fs::read_dir("/var/run") {
                info!("Contents of /var/run:");
                for entry in entries {
                    if let Ok(entry) = entry {
                        info!("  {}", entry.path().display());
                    }
                }
            }
            
            anyhow::bail!("Neither Docker nor Podman socket found. Make sure Docker or Podman is installed and running.")
        };

        info!("Successfully connected to Docker/Podman API");
        Ok(ContainerManager { docker })
    }

    /// Set up a container for STDIO transport
    fn setup_stdio_transport(&self, name: &str) -> Result<()> {
        info!("Setting up STDIO transport for {}", name);
        
        // For STDIO transport, we need to set up a proxy between HTTP SSE and STDIO
        // This will be done after the container is started
        
        Ok(())
    }
    
    /// Set up a proxy between HTTP SSE and container STDIO
    fn setup_stdio_proxy(&self, container_id: &str, name: &str, port: Option<u16>) -> Result<()> {
        // Default to port 8080 if not specified
        let port = port.unwrap_or(8080);
        
        info!("Setting up STDIO proxy for {} on port {}", name, port);
        
        // Start a TCP listener for HTTP SSE connections
        let listener = TcpListener::bind(format!("127.0.0.1:{}", port))
            .context(format!("Failed to bind to port {}", port))?;
        
        info!("Listening for HTTP SSE connections on port {}", port);
        
        // Clone container ID and name for the thread
        let container_id = container_id.to_string();
        let name = name.to_string();
        
        // Create a Docker client for the thread
        let docker = self.docker.clone();
        
        // Spawn a thread to handle incoming connections
        thread::spawn(move || {
            info!("STDIO proxy thread started for {}", name);
            
            // Create a runtime for async operations
            let rt = tokio::runtime::Runtime::new().expect("Failed to create tokio runtime");
            
            // Accept connections
            for stream in listener.incoming() {
                match stream {
                    Ok(stream) => {
                        info!("Accepted HTTP SSE connection for {}", name);
                        
                        // Clone the Docker client and container ID for this connection
                        let docker_clone = docker.clone();
                        let container_id_clone = container_id.clone();
                        
                        // Handle the connection in the current thread
                        // This is a blocking operation, so we'll handle one connection at a time
                        if let Err(e) = rt.block_on(Self::handle_sse_connection(stream, docker_clone, &container_id_clone)) {
                            error!("Error handling HTTP SSE connection: {}", e);
                        }
                    }
                    Err(e) => {
                        error!("Error accepting HTTP SSE connection: {}", e);
                        break;
                    }
                }
            }
            
            info!("STDIO proxy thread exiting for {}", name);
        });
        
        Ok(())
    }
    
    /// Handle an HTTP SSE connection
    async fn handle_sse_connection(stream: TcpStream, docker: Docker, container_id: &str) -> Result<()> {
        use bollard::container::AttachContainerOptions;
        info!("Handling HTTP SSE connection for container {}", container_id);
        
        // Use blocking I/O for reading the HTTP request
        let mut reader = std::io::BufReader::new(stream.try_clone()?);
        let mut request_line = String::new();
        std::io::BufRead::read_line(&mut reader, &mut request_line)?;
        
        info!("Received HTTP request: {}", request_line.trim());
        
        // Read headers until we get an empty line
        let mut headers = Vec::new();
        loop {
            let mut header = String::new();
            std::io::BufRead::read_line(&mut reader, &mut header)?;
            if header == "\r\n" || header == "\n" {
                break;
            }
            headers.push(header);
        }
        
        // Use blocking I/O for writing the response
        let mut writer = stream;
        // Send HTTP SSE response headers
        let response_headers = "HTTP/1.1 200 OK\r\n\
                               Content-Type: text/event-stream\r\n\
                               Cache-Control: no-cache\r\n\
                               Connection: keep-alive\r\n\
                               \r\n";
        
        writer.write_all(response_headers.as_bytes())?;
        writer.flush()?;
        
        // Attach to the container
        let options = AttachContainerOptions::<String> {
            stdin: Some(true),
            stdout: Some(true),
            stderr: Some(true),
            stream: Some(true),
            ..Default::default()
        };
        
        let _attach_stream = docker.attach_container(container_id, Some(options)).await?;
        
        // Create a channel to signal when the connection is closed
        let (tx, mut rx) = tokio::sync::mpsc::channel::<()>(1);
        let _tx_clone = tx.clone();
        
        // Create a thread to forward container output to the client
        let writer_clone = writer.try_clone()?;
        let tx_clone = tx.clone();
        let container_to_client_thread = thread::spawn(move || {
            let mut writer = writer_clone;
            
            // Create a runtime for async operations
            let rt = tokio::runtime::Runtime::new().expect("Failed to create tokio runtime");
            
            // Get the attach stream
            let mut attach_stream = match rt.block_on(async {
                let options = AttachContainerOptions::<String> {
                    stdin: Some(true),
                    stdout: Some(true),
                    stderr: Some(true),
                    stream: Some(true),
                    ..Default::default()
                };
                
                docker.attach_container(container_id, Some(options)).await
            }) {
                Ok(stream) => stream,
                Err(e) => {
                    error!("Failed to attach to container: {}", e);
                    
                    // Send a few heartbeat messages as a fallback
                    for i in 1..=10 {
                        // Format the output as an SSE event
                        let event = format!("event: message\r\ndata: {{\"type\": \"heartbeat\", \"count\": {}}}\r\n\r\n", i);
                        
                        // Send the event to the client
                        if let Err(e) = writer.write_all(event.as_bytes()) {
                            error!("Failed to write to HTTP SSE stream: {}", e);
                            break;
                        }
                        
                        // Flush the writer to ensure the event is sent immediately
                        if let Err(e) = writer.flush() {
                            error!("Failed to flush HTTP SSE stream: {}", e);
                            break;
                        }
                        
                        // Wait for a second
                        std::thread::sleep(std::time::Duration::from_secs(1));
                    }
                    
                    // Signal that the connection is closed
                    rt.block_on(async {
                        let _ = tx_clone.send(()).await;
                    });
                    
                    info!("Container to client task exiting (fallback mode)");
                    return;
                }
            };
            
            // Forward container output to the client
            rt.block_on(async {
                let mut buffer = [0u8; 4096];
                
                loop {
                    match attach_stream.read(&mut buffer).await {
                        Ok(0) => {
                            // End of stream
                            info!("Container stream ended");
                            break;
                        }
                        Ok(n) => {
                            // Format the output as an SSE event
                            let output = String::from_utf8_lossy(&buffer[..n]);
                            let event = format!("event: message\r\ndata: {}\r\n\r\n", output);
                            
                            // Send the event to the client
                            if let Err(e) = writer.write_all(event.as_bytes()) {
                                error!("Failed to write to HTTP SSE stream: {}", e);
                                break;
                            }
                            
                            // Flush the writer to ensure the event is sent immediately
                            if let Err(e) = writer.flush() {
                                error!("Failed to flush HTTP SSE stream: {}", e);
                                break;
                            }
                        }
                        Err(e) => {
                            error!("Failed to read from container: {}", e);
                            break;
                        }
                    }
                }
            });
            
            // Signal that the connection is closed
            let rt = tokio::runtime::Runtime::new().expect("Failed to create tokio runtime");
            rt.block_on(async {
                let _ = tx_clone.send(()).await;
            });
            
            info!("Container to client task exiting");
        });
        
        // Create a thread to forward messages from client to container
        let reader_clone = reader.into_inner();
        let client_to_container_thread = thread::spawn(move || {
            let mut reader = std::io::BufReader::new(reader_clone);
            let mut buffer = [0u8; 4096];
            
            // Create a runtime for async operations
            let rt = tokio::runtime::Runtime::new().expect("Failed to create tokio runtime");
            
            // Get the attach stream
            let mut attach_stream = match rt.block_on(async {
                let options = AttachContainerOptions::<String> {
                    stdin: Some(true),
                    stdout: Some(true),
                    stderr: Some(true),
                    stream: Some(true),
                    ..Default::default()
                };
                
                docker.attach_container(container_id, Some(options)).await
            }) {
                Ok(stream) => stream,
                Err(e) => {
                    error!("Failed to attach to container: {}", e);
                    
                    // Signal that the connection is closed
                    rt.block_on(async {
                        let _ = tx.send(()).await;
                    });
                    
                    info!("Client to container task exiting (fallback mode)");
                    return;
                }
            };
            
            loop {
                // Read from the client
                match reader.read(&mut buffer) {
                    Ok(0) => {
                        // End of stream
                        info!("Client disconnected");
                        break;
                    }
                    Ok(n) => {
                        // Parse the client message and forward it to the container
                        let message = String::from_utf8_lossy(&buffer[..n]);
                        info!("Received message from client: {}", message);
                        
                        // Forward the message to the container
                        rt.block_on(async {
                            if let Err(e) = attach_stream.write_all(&buffer[..n]).await {
                                error!("Failed to write to container: {}", e);
                            }
                            
                            if let Err(e) = attach_stream.flush().await {
                                error!("Failed to flush container stream: {}", e);
                            }
                        });
                    }
                    Err(e) => {
                        error!("Failed to read from client: {}", e);
                        break;
                    }
                }
            }
            
            // Signal that the connection is closed
            let rt = tokio::runtime::Runtime::new().expect("Failed to create tokio runtime");
            rt.block_on(async {
                let _ = tx.send(()).await;
            });
            
            info!("Client to container task exiting");
        });
        
        // Wait for the connection to close
        let _ = rx.recv().await;
        info!("Connection closed");
        
        // Wait for the threads to finish
        if let Err(e) = container_to_client_thread.join() {
            error!("Error joining container to client thread: {:?}", e);
        }
        
        if let Err(e) = client_to_container_thread.join() {
            error!("Error joining client to container thread: {:?}", e);
        }
        
        info!("HTTP SSE connection handler exiting");
        Ok(())
    }

    /// Run an MCP server container
    pub async fn run_container(
        &self,
        name: &str,
        image: &str,
        transport: TransportMode,
        port: Option<u16>,
        profile: &PermissionProfile,
        args: &[String],
        _detach: bool,
    ) -> Result<String> {
        // Validate inputs
        if transport == TransportMode::SSE && port.is_none() {
            anyhow::bail!("Port is required for SSE transport");
        }

        // Prepare container labels
        let mut labels = HashMap::new();
        labels.insert("mcp-lok".to_string(), "true".to_string());
        labels.insert("mcp-lok-name".to_string(), name.to_string());
        labels.insert(
            "mcp-lok-transport".to_string(),
            format!("{:?}", transport).to_lowercase(),
        );

        // Prepare host config based on transport mode and permissions
        let host_config = self.create_host_config(name, transport, port, profile)?;

        // Prepare container config
        let mut cmd = Vec::new();
        if !args.is_empty() {
            cmd.extend(args.iter().cloned());
        }

        let config = Config {
            image: Some(image.to_string()),
            cmd: if cmd.is_empty() { None } else { Some(cmd) },
            host_config: Some(host_config),
            labels: Some(labels),
            ..Default::default()
        };

        // If using STDIO transport, set it up
        if transport == TransportMode::STDIO {
            if let Err(e) = self.setup_stdio_transport(name) {
                error!("Failed to set up STDIO transport: {}", e);
                return Err(e);
            }
        }
        // Create container
        let options = CreateContainerOptions {
            name,
            platform: None,
        };

        let response = self
            .docker
            .create_container(Some(options), config)
            .await
            .context("Failed to create container")?;

        // Start container
        self.docker
            .start_container(&response.id, None::<StartContainerOptions<String>>)
            .await
            .context("Failed to start container")?;

        info!("Started MCP server container: {}", name);
        debug!("Container ID: {}", response.id);
        
        // If using STDIO transport, set up the proxy
        if transport == TransportMode::STDIO {
            if let Err(e) = self.setup_stdio_proxy(&response.id, name, port) {
                error!("Failed to set up STDIO proxy: {}", e);
                // Try to stop and remove the container if proxy setup fails
                let _ = self.docker.stop_container(&response.id, None::<StopContainerOptions>).await;
                let _ = self.docker.remove_container(&response.id, None::<RemoveContainerOptions>).await;
                return Err(e);
            }
            
            // If not in detached mode, keep the main command running
            if !_detach {
                info!("Press Ctrl+C to stop the MCP server");
                
                // Create a channel to wait for Ctrl+C
                let (tx, rx) = tokio::sync::oneshot::channel::<()>();
                
                // Handle Ctrl+C
                let container_id = response.id.clone();
                let docker_clone = self.docker.clone();
                tokio::spawn(async move {
                    if let Err(e) = tokio::signal::ctrl_c().await {
                        error!("Failed to listen for Ctrl+C: {}", e);
                        return;
                    }
                    
                    info!("Received Ctrl+C, stopping MCP server");
                    
                    // Stop and remove the container
                    let _ = docker_clone.stop_container(&container_id, None::<StopContainerOptions>).await;
                    let _ = docker_clone.remove_container(&container_id, None::<RemoveContainerOptions>).await;
                    
                    // Signal that we're done
                    let _ = tx.send(());
                });
                
                // Wait for Ctrl+C
                let _ = rx.await;
            }
        }

        Ok(response.id)
    }

    /// Create host config based on transport mode and permissions
    fn create_host_config(
        &self,
        _name: &str,
        transport: TransportMode,
        port: Option<u16>,
        profile: &PermissionProfile,
    ) -> Result<HostConfig> {
        let mut host_config = HostConfig::default();

        // Set up mounts
        let mut mounts = Vec::new();

        // For STDIO transport, we don't need to add any special mounts
        // The container will use its stdin/stdout for communication

        // Add additional mounts from permission profile
        for path in &profile.read {
            if !profile.write.contains(path) {
                // Read-only mount
                mounts.push(Mount {
                    target: Some(path.clone()),
                    source: Some(path.clone()),
                    typ: Some(MountTypeEnum::BIND),
                    read_only: Some(true),
                    ..Default::default()
                });
            }
        }

        for path in &profile.write {
            // Read-write mount
            mounts.push(Mount {
                target: Some(path.clone()),
                source: Some(path.clone()),
                typ: Some(MountTypeEnum::BIND),
                read_only: Some(false),
                ..Default::default()
            });
        }

        host_config.mounts = Some(mounts);

        // Set up port bindings for SSE transport
        if transport == TransportMode::SSE {
            if let Some(port) = port {
                let mut port_bindings = HashMap::new();
                let binding = vec![PortBinding {
                    host_ip: Some("127.0.0.1".to_string()),
                    host_port: Some(port.to_string()),
                }];
                port_bindings.insert(format!("{}/tcp", port), Some(binding));
                host_config.port_bindings = Some(port_bindings);
            }
        }

        // Set up network restrictions based on permission profile
        if let Some(network) = &profile.network {
            if !network.outbound.insecure_allow_all {
                // TODO: Implement network restrictions
                // This would require setting up network namespaces and iptables rules
                // which is beyond the scope of this basic implementation
            }
        }

        Ok(host_config)
    }

    /// List running MCP server containers
    pub async fn list_containers(&self) -> Result<Vec<(String, String)>> {
        let mut filters = HashMap::new();
        filters.insert("label".to_string(), vec!["mcp-lok=true".to_string()]);
        
        let options = ListContainersOptions {
            all: true,
            filters,
            ..Default::default()
        };

        let containers = self
            .docker
            .list_containers(Some(options))
            .await
            .context("Failed to list containers")?;

        let mut result = Vec::new();
        for container in containers {
            if let (Some(id), Some(labels)) = (container.id, container.labels) {
                if let Some(name) = labels.get("mcp-lok-name") {
                    result.push((name.clone(), id));
                }
            }
        }

        Ok(result)
    }

    /// Stop an MCP server container
    pub async fn stop_container(&self, name: &str) -> Result<()> {
        let containers = self.list_containers().await?;
        
        for (container_name, id) in containers {
            if container_name == name {
                self.docker
                    .stop_container(&id, Some(StopContainerOptions { t: 10 }))
                    .await
                    .context("Failed to stop container")?;
                
                info!("Stopped MCP server container: {}", name);
                return Ok(());
            }
        }
        
        anyhow::bail!("Container not found: {}", name)
    }

    /// Remove an MCP server container
    pub async fn remove_container(&self, name: &str) -> Result<()> {
        let containers = self.list_containers().await?;
        
        for (container_name, id) in containers {
            if container_name == name {
                let options = RemoveContainerOptions {
                    force: true,
                    ..Default::default()
                };
                
                self.docker
                    .remove_container(&id, Some(options))
                    .await
                    .context("Failed to remove container")?;
                
                info!("Removed MCP server container: {}", name);
                return Ok(());
            }
        }
        
        anyhow::bail!("Container not found: {}", name)
    }
}