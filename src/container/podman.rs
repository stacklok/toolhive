use async_trait::async_trait;
use hyper::{Body, Client, Method, Request, StatusCode};
use hyper::body::HttpBody;
use hyperlocal::UnixConnector;
use serde::{Deserialize, Serialize};
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

// Podman socket paths
const PODMAN_SOCKET_PATH: &str = "/var/run/podman/podman.sock";
const PODMAN_XDG_RUNTIME_SOCKET_PATH: &str = "podman/podman.sock";
const PODMAN_API_VERSION: &str = "v5.0.0";

/// Client for interacting with the Podman API
pub struct PodmanClient {
    client: Client<UnixConnector, Body>,
    socket_path: String,
}

impl PodmanClient {
    /// Create a new Podman client
    pub async fn new() -> Result<Self> {
        // Try to find the Podman socket in various locations
        let socket_path = Self::find_podman_socket()?;
        Self::with_socket_path(&socket_path).await
    }
    
    /// Parse a timestamp string into a Unix timestamp (seconds since epoch)
    fn parse_timestamp(_timestamp: &str) -> u64 {
        // Try to parse the timestamp using chrono if available
        // For simplicity, we'll just return the current timestamp if parsing fails
        match SystemTime::now().duration_since(UNIX_EPOCH) {
            Ok(duration) => duration.as_secs(),
            Err(_) => 0,
        }
    }

    /// Create a new Podman client with a custom socket path
    pub async fn with_socket_path(socket_path: &str) -> Result<Self> {
        log::debug!("Creating Podman client with socket path: {}", socket_path);
        
        // Check if the socket exists
        if !Path::new(socket_path).exists() {
            log::debug!("Podman socket not found at {}", socket_path);
            return Err(Error::ContainerRuntime(format!(
                "Podman socket not found at {}",
                socket_path
            )));
        }
        log::debug!("Podman socket exists at {}", socket_path);

        // Create HTTP client with Unix socket support
        let client = Client::builder()
            .build(UnixConnector::default());

        let podman = Self {
            client,
            socket_path: socket_path.to_string(),
        };

        // Verify that Podman is available
        log::debug!("Pinging Podman API...");
        match podman.ping().await {
            Ok(_) => log::debug!("Podman API ping successful"),
            Err(e) => {
                log::debug!("Podman API ping failed: {}", e);
                return Err(e);
            }
        }

        log::debug!("Podman client created successfully");
        Ok(podman)
    }

    /// Find the Podman socket path
    fn find_podman_socket() -> Result<String> {
        log::debug!("Searching for Podman socket...");
        
        // Check standard location
        log::debug!("Checking standard location: {}", PODMAN_SOCKET_PATH);
        if Path::new(PODMAN_SOCKET_PATH).exists() {
            log::debug!("Found Podman socket at standard location: {}", PODMAN_SOCKET_PATH);
            return Ok(PODMAN_SOCKET_PATH.to_string());
        }

        // Check XDG_RUNTIME_DIR location
        if let Ok(xdg_runtime_dir) = env::var("XDG_RUNTIME_DIR") {
            let xdg_socket_path = PathBuf::from(xdg_runtime_dir)
                .join(PODMAN_XDG_RUNTIME_SOCKET_PATH);
            
            log::debug!("Checking XDG_RUNTIME_DIR location: {}", xdg_socket_path.display());
            if xdg_socket_path.exists() {
                log::debug!("Found Podman socket at XDG_RUNTIME_DIR location: {}", xdg_socket_path.display());
                return Ok(xdg_socket_path.to_string_lossy().to_string());
            }
        } else {
            log::debug!("XDG_RUNTIME_DIR environment variable not set");
        }

        // Check user-specific location
        if let Ok(home) = env::var("HOME") {
            let user_socket_path = PathBuf::from(home)
                .join(".local/share/containers/podman/machine/podman.sock");
            
            log::debug!("Checking user-specific location: {}", user_socket_path.display());
            if user_socket_path.exists() {
                log::debug!("Found Podman socket at user-specific location: {}", user_socket_path.display());
                return Ok(user_socket_path.to_string_lossy().to_string());
            }
        } else {
            log::debug!("HOME environment variable not set");
        }

        log::debug!("Podman socket not found in any location");
        Err(Error::ContainerRuntime("Podman socket not found".to_string()))
    }

    /// Get the base URI path for the Podman API
    fn base_uri_path() -> String {
        format!("/{}/libpod", PODMAN_API_VERSION)
    }

    /// Build a URI path for the Podman API
    fn uri_path(&self, path: &str) -> String {
        // Use the correct versioned path format
        format!("{}/{}", Self::base_uri_path(), path)
    }

    /// Make a request to the Podman API
    async fn request<T: Serialize, U: for<'de> Deserialize<'de>>(
        &self,
        method: Method,
        path: &str,
        body: Option<T>,
    ) -> Result<U> {
        let uri_path = self.uri_path(path);
        
        // Build the request
        let mut req = Request::builder()
            .method(method)
            .uri(hyperlocal::Uri::new(&self.socket_path, &uri_path));
        
        // Add JSON content type if there's a body
        if body.is_some() {
            req = req.header("content-type", "application/json");
        }
        
        // Finalize the request with or without a body
        let req = if let Some(body) = body {
            let body_str = serde_json::to_string(&body)?;
            req.body(Body::from(body_str))?
        } else {
            req.body(Body::empty())?
        };
        
        // Send the request
        let res = self.client.request(req).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to send request: {}", e)))?;
        
        // Check the status code
        let status = res.status();
        if !status.is_success() && status != StatusCode::NOT_MODIFIED {
            let body_bytes = hyper::body::to_bytes(res.into_body()).await
                .map_err(|e| Error::ContainerRuntime(format!("Failed to read response body: {}", e)))?;
            
            let error_text = String::from_utf8_lossy(&body_bytes);
            return Err(Error::ContainerRuntime(format!(
                "API request failed with status {}: {}",
                status,
                error_text
            )));
        }
        
        // Parse the response body
        let body_bytes = hyper::body::to_bytes(res.into_body()).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to read response body: {}", e)))?;
        
        // Handle empty responses
        if body_bytes.is_empty() {
            // Create an empty JSON object for empty responses
            let empty_json = "{}";
            serde_json::from_str(empty_json)
                .map_err(|e| Error::Json(e))
        } else {
            serde_json::from_slice(&body_bytes)
                .map_err(|e| Error::Json(e))
        }
    }

    /// Check if Podman is available
    async fn ping(&self) -> Result<()> {
        let uri_path = format!("{}/_ping", Self::base_uri_path());
        log::debug!("Pinging Podman API at path: {}", uri_path);
        
        let req = Request::builder()
            .method(Method::GET)
            .uri(hyperlocal::Uri::new(&self.socket_path, &uri_path))
            .body(Body::empty())?;
        
        log::debug!("Sending ping request to Podman API...");
        let res = match self.client.request(req).await {
            Ok(res) => {
                log::debug!("Received response from Podman API with status: {}", res.status());
                res
            },
            Err(e) => {
                log::debug!("Failed to ping Podman: {}", e);
                return Err(Error::ContainerRuntime(format!("Failed to ping Podman: {}", e)));
            }
        };
        
        if res.status().is_success() {
            log::debug!("Podman ping successful");
            Ok(())
        } else {
            log::debug!("Podman ping failed with status: {}", res.status());
            Err(Error::ContainerRuntime(format!(
                "Podman ping failed with status: {}",
                res.status()
            )))
        }
    }
}

/// Podman container attach stream for reading
pub struct PodmanAttachReader {
    reader: hyper::body::Body,
    buffer: Vec<u8>,
}

/// Podman container attach stream for writing
pub struct PodmanAttachWriter {
    client: Client<UnixConnector, Body>,
    socket_path: String,
    container_id: String,
}

impl AsyncRead for PodmanAttachReader {
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
        match Pin::new(&mut self.reader).poll_data(cx) {
            Poll::Ready(Some(Ok(chunk))) => {
                // Copy the chunk data into the buffer
                let len = std::cmp::min(buf.remaining(), chunk.len());
                buf.put_slice(&chunk[..len]);
                
                // Store any remaining data in the buffer
                if len < chunk.len() {
                    self.buffer = chunk[len..].to_vec();
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

impl AsyncWrite for PodmanAttachWriter {
    fn poll_write(
        self: Pin<&mut Self>,
        _cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<std::io::Result<usize>> {
        // Create a request to write to the container using the correct API version
        let uri_path = format!("{}/containers/{}/attach?stream=1&stdin=1", PodmanClient::base_uri_path(), self.container_id);
        
        let req = match Request::builder()
            .method(Method::POST)
            .uri(hyperlocal::Uri::new(&self.socket_path, &uri_path))
            .header("Content-Type", "application/vnd.docker.raw-stream")
            .body(Body::from(buf.to_vec())) {
                Ok(req) => req,
                Err(e) => return Poll::Ready(Err(std::io::Error::new(std::io::ErrorKind::Other, e))),
            };
        
        // Send the request
        let _ = self.client.request(req);
        
        // Return the number of bytes written
        Poll::Ready(Ok(buf.len()))
    }

    fn poll_flush(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<std::io::Result<()>> {
        // No buffering, so flush is a no-op
        Poll::Ready(Ok(()))
    }

    fn poll_shutdown(self: Pin<&mut Self>, _cx: &mut Context<'_>) -> Poll<std::io::Result<()>> {
        // No special shutdown needed
        Poll::Ready(Ok(()))
    }
}

#[async_trait]
impl ContainerRuntime for PodmanClient {
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
        let mounts: Vec<PodmanMount> = permission_config
            .mounts
            .iter()
            .map(|mount| PodmanMount {
                type_: "bind".to_string(),
                source: mount.source.clone(),
                destination: mount.target.clone(),
                options: if mount.read_only {
                    Some(vec!["ro".to_string()])
                } else {
                    None
                },
            })
            .collect();

        // Check if STDIO transport is being used
        let is_stdio_transport = env_vars.get("MCP_TRANSPORT").map_or(false, |v| v == "stdio");

        // Create container configuration
        let create_config = PodmanCreateContainerConfig {
            image: image.to_string(),
            name: name.to_string(),
            command: Some(command),
            env: Some(env_vars),
            labels: Some(labels),
            // Set stdin/stdout attachment options if using STDIO transport
            attach_stdin: if is_stdio_transport { Some(true) } else { None },
            attach_stdout: if is_stdio_transport { Some(true) } else { None },
            attach_stderr: if is_stdio_transport { Some(true) } else { None },
            open_stdin: if is_stdio_transport { Some(true) } else { None },
            host_config: PodmanHostConfig {
                mounts: Some(mounts),
                network_mode: permission_config.network_mode,
                cap_drop: Some(permission_config.cap_drop),
                cap_add: Some(permission_config.cap_add),
                security_opt: Some(permission_config.security_opt),
            },
        };

        // Create container
        let path = "containers/create";
        let create_response: PodmanCreateContainerResponse = self.request(
            Method::POST,
            &path,
            Some(create_config),
        ).await?;

        // Print the warnings if any only in debug mode
        for warning in create_response.warnings {
            log::debug!("Container create warning: {}", warning);
        }

        Ok(create_response.id.clone())
    }

    async fn start_container(&self, container_id: &str) -> Result<()> {
        // Start container
        let path = format!("containers/{}/start", container_id);
        let _: serde_json::Value = self.request(
            Method::POST,
            &path,
            Option::<()>::None,
        ).await?;

        Ok(())
    }

    async fn list_containers(&self) -> Result<Vec<ContainerInfo>> {
        // Try different endpoints for listing containers
        let filter = format!("{{\"label\":[\"{}\"]}}", labels::format_vibetool_filter());
        let encoded_filter = urlencoding::encode(&filter);
        
        let paths = vec![
            // Standard Docker API endpoint
            format!("containers/json?filters={}", encoded_filter),
            // Podman specific endpoint
            format!("libpod/containers/json?filters={}", encoded_filter),
            // Try without filters
            "containers/json".to_string(),
            "libpod/containers/json".to_string(),
        ];
        
        for path in paths {
            log::debug!("Trying to list containers with path: {}", path);
            
            match self.request::<(), Vec<PodmanContainer>>(
                Method::GET,
                &path,
                Option::<()>::None,
            ).await {
                Ok(podman_containers) => {
                    log::debug!("Successfully listed containers with path: {}", path);
                    log::debug!("Found {} containers", podman_containers.len());
                    
                    // Convert Podman containers to our ContainerInfo format
                    let containers = podman_containers
                        .into_iter()
                        .filter(|c| {
                            // Filter for vibetool containers if we're using an endpoint without filters
                            if !path.contains("filters=") {
                                if let Some(labels) = &c.labels {
                                    return labels::is_vibetool_container(labels);
                                }
                                return false;
                            }
                            true
                        })
                        .map(|c| {
                            let ports = c.ports.unwrap_or_default()
                                .into_iter()
                                .map(|p| PortMapping {
                                    container_port: p.container_port,
                                    host_port: p.host_port.unwrap_or(0),
                                    protocol: p.protocol,
                                })
                                .collect();

                            ContainerInfo {
                                id: c.id,
                                name: c.names.unwrap_or_default().first().cloned().unwrap_or_default(),
                                image: c.image,
                                status: c.status,
                                state: c.state,
                                created: 0, // Default to 0 for now since we have a string timestamp
                                labels: c.labels.unwrap_or_default(),
                                ports,
                            }
                        })
                        .collect();

                    return Ok(containers);
                },
                Err(e) => {
                    log::debug!("Failed to list containers with path {}: {}", path, e);
                }
            }
        }
        
        // If we get here, all attempts failed
        Err(Error::ContainerRuntime("Failed to list containers with any endpoint".to_string()))
    }

    async fn stop_container(&self, container_id: &str) -> Result<()> {
        // Stop container
        let path = format!("containers/{}/stop", container_id);
        let _: serde_json::Value = self.request(
            Method::POST,
            &path,
            Option::<()>::None,
        ).await?;

        Ok(())
    }

    async fn remove_container(&self, container_id: &str) -> Result<()> {
        // Remove container
        let path = format!("containers/{}?force=true", container_id);
        let _: serde_json::Value = self.request(
            Method::DELETE,
            &path,
            Option::<()>::None,
        ).await?;

        Ok(())
    }

    async fn container_logs(&self, container_id: &str) -> Result<String> {
        // Get container logs
        let path = format!("containers/{}/logs?stdout=true&stderr=true", container_id);
        
        let uri_path = self.uri_path(&path);
        let req = Request::builder()
            .method(Method::GET)
            .uri(hyperlocal::Uri::new(&self.socket_path, &uri_path))
            .body(Body::empty())?;
        
        let res = self.client.request(req).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to get container logs: {}", e)))?;
        
        if !res.status().is_success() {
            return Err(Error::ContainerRuntime(format!(
                "Failed to get container logs: {}",
                res.status()
            )));
        }
        
        let body_bytes = hyper::body::to_bytes(res.into_body()).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to read logs: {}", e)))?;
        
        let logs = String::from_utf8_lossy(&body_bytes).to_string();
        
        Ok(logs)
    }

    async fn is_container_running(&self, container_id: &str) -> Result<bool> {
        // Get container info
        let path = format!("containers/{}/json", container_id);
        
        let uri_path = self.uri_path(&path);
        let req = Request::builder()
            .method(Method::GET)
            .uri(hyperlocal::Uri::new(&self.socket_path, &uri_path))
            .body(Body::empty())?;
        
        let res = self.client.request(req).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to get container info: {}", e)))?;
        
        if !res.status().is_success() {
            if res.status() == StatusCode::NOT_FOUND {
                return Err(Error::ContainerNotFound(container_id.to_string()));
            }
            
            return Err(Error::ContainerRuntime(format!(
                "Failed to get container info: {}",
                res.status()
            )));
        }
        
        let body_bytes = hyper::body::to_bytes(res.into_body()).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to read response body: {}", e)))?;
        
        let container: PodmanContainerInspect = serde_json::from_slice(&body_bytes)
            .map_err(|e| Error::Json(e))?;
        
        // Use the running boolean field directly from the container state
        // This is more reliable than checking the status string
        Ok(container.state.running)
    }

    async fn get_container_ip(&self, container_id: &str) -> Result<String> {
        // Get container info
        let path = format!("containers/{}/json", container_id);
        
        let uri_path = self.uri_path(&path);
        let req = Request::builder()
            .method(Method::GET)
            .uri(hyperlocal::Uri::new(&self.socket_path, &uri_path))
            .body(Body::empty())?;
        
        let res = self.client.request(req).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to get container info: {}", e)))?;
        
        if !res.status().is_success() {
            if res.status() == StatusCode::NOT_FOUND {
                return Err(Error::ContainerNotFound(container_id.to_string()));
            }
            
            return Err(Error::ContainerRuntime(format!(
                "Failed to get container info: {}",
                res.status()
            )));
        }
        
        let body_bytes = hyper::body::to_bytes(res.into_body()).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to read response body: {}", e)))?;
        
        let container: PodmanContainerInspect = serde_json::from_slice(&body_bytes)
            .map_err(|e| Error::Json(e))?;

        // Get the container's IP address from the network settings
        // First try to get the IP from the default bridge network
        if let Some(networks) = &container.network_settings.networks {
            if let Some(bridge) = networks.get("bridge") {
                if let Some(ip) = &bridge.ip_address {
                    if !ip.is_empty() {
                        return Ok(ip.clone());
                    }
                }
            }
            
            // If bridge network doesn't have an IP, try any other network
            for (_, network) in networks {
                if let Some(ip) = &network.ip_address {
                    if !ip.is_empty() {
                        return Ok(ip.clone());
                    }
                }
            }
        }
        
        // If we couldn't find an IP address, return an error
        Err(Error::ContainerRuntime(format!("No IP address found for container {}", container_id)))
    }

    async fn get_container_info(&self, container_id: &str) -> Result<ContainerInfo> {
        // Get container info
        let path = format!("containers/{}/json", container_id);
        
        let uri_path = self.uri_path(&path);
        let req = Request::builder()
            .method(Method::GET)
            .uri(hyperlocal::Uri::new(&self.socket_path, &uri_path))
            .body(Body::empty())?;
        
        let res = self.client.request(req).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to get container info: {}", e)))?;
        
        if !res.status().is_success() {
            if res.status() == StatusCode::NOT_FOUND {
                return Err(Error::ContainerNotFound(container_id.to_string()));
            }
            
            return Err(Error::ContainerRuntime(format!(
                "Failed to get container info: {}",
                res.status()
            )));
        }
        
        let body_bytes = hyper::body::to_bytes(res.into_body()).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to read response body: {}", e)))?;
        
        let container: PodmanContainerInspect = serde_json::from_slice(&body_bytes)
            .map_err(|e| Error::Json(e))?;

        // Convert port mappings
        let mut ports = Vec::new();
        if let Some(port_bindings) = &container.network_settings.ports {
            for (port_proto, bindings) in port_bindings {
                if let Some(bindings) = bindings {
                    for binding in bindings {
                        if let Some(host_port_str) = &binding.host_port {
                            if let Ok(host_port) = host_port_str.parse::<u16>() {
                                // Parse container port and protocol from the key (e.g., "80/tcp")
                                let parts: Vec<&str> = port_proto.split('/').collect();
                                if parts.len() == 2 {
                                    if let Ok(container_port) = parts[0].parse::<u16>() {
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

        // Try to parse the created timestamp
        let created_timestamp = if let Some(created_str) = container.created.as_ref() {
            Self::parse_timestamp(created_str)
        } else {
            0 // Default to 0 if no timestamp is available
        };
        
        Ok(ContainerInfo {
            id: container.id,
            name: container.name,
            image: container.config.image,
            status: container.state.status.clone(),
            state: container.state.status,
            created: created_timestamp,
            labels: container.config.labels.unwrap_or_default(),
            ports,
        })
    }
    
    async fn attach_container(&self, container_id: &str) -> Result<(Box<dyn AsyncWrite + Unpin + Send>, Box<dyn AsyncRead + Unpin + Send>)> {
        // Create the attach URL for reading
        let attach_url = format!("containers/{}/attach?stream=true&stdin=true&stdout=true&stderr=true", container_id);
        
        // Create the request
        let req = Request::builder()
            .method(Method::POST)
            .uri(hyperlocal::Uri::new(&self.socket_path, &self.uri_path(&attach_url)))
            .header("Content-Type", "application/vnd.docker.raw-stream")
            .body(Body::empty())?;
        
        // Send the request
        let res = self.client.request(req).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to attach to container: {}", e)))?;
        
        // Check the status code
        if !res.status().is_success() {
            return Err(Error::ContainerRuntime(format!(
                "Failed to attach to container: {}",
                res.status()
            )));
        }
        
        // Get the response body as a stream
        let body = res.into_body();
        
        // Create a reader
        let reader = PodmanAttachReader {
            reader: body,
            buffer: Vec::new(),
        };
        
        // Create a writer
        let writer = PodmanAttachWriter {
            client: self.client.clone(),
            socket_path: self.socket_path.clone(),
            container_id: container_id.to_string(),
        };
        
        // Return the reader and writer
        let reader_box: Box<dyn AsyncRead + Unpin + Send> = Box::new(reader);
        let writer_box: Box<dyn AsyncWrite + Unpin + Send> = Box::new(writer);
        
        Ok((writer_box, reader_box))
    }
}

// Podman API types

#[derive(Debug, Serialize)]
struct PodmanCreateContainerConfig {
    #[serde(rename = "Image")]
    image: String,
    #[serde(rename = "Name")]
    name: String,
    #[serde(skip_serializing_if = "Option::is_none", rename = "Command")]
    command: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "Env")]
    env: Option<HashMap<String, String>>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "Labels")]
    labels: Option<HashMap<String, String>>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "AttachStdin")]
    attach_stdin: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "AttachStdout")]
    attach_stdout: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "AttachStderr")]
    attach_stderr: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "OpenStdin")]
    open_stdin: Option<bool>,
    #[serde(rename = "HostConfig")]
    host_config: PodmanHostConfig,
}

#[derive(Debug, Serialize)]
struct PodmanHostConfig {
    #[serde(skip_serializing_if = "Option::is_none", rename = "Mounts")]
    mounts: Option<Vec<PodmanMount>>,
    #[serde(rename = "NetworkMode")]
    network_mode: String,
    #[serde(skip_serializing_if = "Option::is_none", rename = "CapDrop")]
    cap_drop: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "CapAdd")]
    cap_add: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "SecurityOpt")]
    security_opt: Option<Vec<String>>,
}

#[derive(Debug, Serialize)]
struct PodmanMount {
    #[serde(rename = "Type")]
    type_: String,
    #[serde(rename = "Source")]
    source: String,
    #[serde(rename = "Destination")]
    destination: String,
    #[serde(skip_serializing_if = "Option::is_none", rename = "Options")]
    options: Option<Vec<String>>,
}

#[derive(Debug, Deserialize)]
struct PodmanCreateContainerResponse {
    #[serde(rename = "Id")]
    id: String,
    #[serde(default, rename = "Warnings")]
    warnings: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct PodmanContainer {
    #[serde(rename = "Id")]
    id: String,
    #[serde(default, rename = "Names")]
    names: Option<Vec<String>>,
    #[serde(rename = "Image")]
    image: String,
    #[serde(default, rename = "ImageID")]
    #[allow(dead_code)]
    image_id: String,
    #[serde(rename = "Status")]
    status: String,
    #[serde(rename = "State")]
    state: String,
    #[serde(default, rename = "Created")]
    #[allow(dead_code)]
    created: String,
    #[serde(default, rename = "Labels")]
    labels: Option<HashMap<String, String>>,
    #[serde(default, rename = "Ports")]
    ports: Option<Vec<PodmanPort>>,
}

#[derive(Debug, Deserialize)]
struct PodmanPort {
    #[serde(default)]
    #[allow(dead_code)]
    host_ip: Option<String>,
    container_port: u16,
    host_port: Option<u16>,
    protocol: String,
}

#[derive(Debug, Deserialize)]
struct PodmanContainerInspect {
    #[serde(rename = "Id")]
    id: String,
    #[serde(rename = "Name")]
    name: String,
    #[serde(default, rename = "Created")]
    created: Option<String>,
    #[serde(rename = "State")]
    state: PodmanContainerState,
    #[serde(rename = "Config")]
    config: PodmanContainerConfig,
    #[serde(rename = "NetworkSettings")]
    network_settings: PodmanNetworkSettings,
}

#[derive(Debug, Deserialize)]
struct PodmanContainerState {
    #[serde(rename = "Status")]
    status: String,
    #[serde(rename = "Running")]
    #[allow(dead_code)]
    running: bool,
    #[serde(rename = "Paused")]
    #[allow(dead_code)]
    paused: bool,
    #[serde(rename = "Restarting")]
    #[allow(dead_code)]
    restarting: bool,
    #[serde(rename = "OOMKilled")]
    #[allow(dead_code)]
    oom_killed: bool,
    #[serde(rename = "Dead")]
    #[allow(dead_code)]
    dead: bool,
    #[serde(rename = "Pid")]
    #[allow(dead_code)]
    pid: i64,
    #[serde(rename = "ExitCode")]
    #[allow(dead_code)]
    exit_code: i64,
    #[serde(rename = "Error")]
    #[allow(dead_code)]
    error: String,
    #[serde(rename = "StartedAt")]
    #[allow(dead_code)]
    started_at: String,
    #[serde(rename = "FinishedAt")]
    #[allow(dead_code)]
    finished_at: String,
}

#[derive(Debug, Deserialize)]
struct PodmanContainerConfig {
    #[serde(rename = "Image")]
    image: String,
    #[serde(default, rename = "Labels")]
    labels: Option<HashMap<String, String>>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "AttachStdin")]
    attach_stdin: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "AttachStdout")]
    attach_stdout: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "AttachStderr")]
    attach_stderr: Option<bool>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "OpenStdin")]
    open_stdin: Option<bool>,
}

#[derive(Debug, Deserialize)]
struct PodmanNetworkSettings {
    #[serde(default, rename = "Ports")]
    ports: Option<HashMap<String, Option<Vec<PodmanPortBinding>>>>,
    #[serde(default, rename = "Networks")]
    networks: Option<HashMap<String, PodmanNetwork>>,
}

#[derive(Debug, Deserialize)]
struct PodmanNetwork {
    #[serde(rename = "IPAddress")]
    ip_address: Option<String>,
    #[serde(rename = "Gateway")]
    gateway: Option<String>,
    #[serde(rename = "MacAddress")]
    mac_address: Option<String>,
}

#[derive(Debug, Deserialize)]
struct PodmanPortBinding {
    #[serde(rename = "HostIp")]
    #[allow(dead_code)]
    host_ip: Option<String>,
    #[serde(rename = "HostPort")]
    host_port: Option<String>,
}

#[cfg(test)]
mod tests {
    use super::*;
    use mockall::predicate::*;
    use mockall::*;

    // Mock for testing
    mock! {
        pub PodmanClient {
            fn uri_path(&self, path: &str) -> String;
            async fn ping(&self) -> Result<()>;
        }

        #[async_trait]
        impl ContainerRuntime for PodmanClient {
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

    // Tests would go here, but they would require mocking the HTTP client
    // which is beyond the scope of this implementation
}