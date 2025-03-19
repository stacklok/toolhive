use async_trait::async_trait;
use hyper::{Body, Client, Method, Request, StatusCode};
use hyper::body::HttpBody;
use hyperlocal::UnixConnector;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::Path;
use std::pin::Pin;
use std::task::{Context, Poll};
use std::time::{SystemTime, UNIX_EPOCH};
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};

use crate::container::{ContainerInfo, ContainerRuntime, PortMapping};
use crate::error::{Error, Result};
use crate::permissions::profile::ContainerPermissionConfig;

const DOCKER_SOCKET_PATH: &str = "/var/run/docker.sock";
const DOCKER_API_VERSION: &str = "v1.41";

/// Client for interacting with the Docker API
pub struct DockerClient {
    client: Client<UnixConnector, Body>,
    socket_path: String,
}

impl DockerClient {
    /// Create a new Docker client
    pub async fn new() -> Result<Self> {
        Self::with_socket_path(DOCKER_SOCKET_PATH).await
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

    /// Create a new Docker client with a custom socket path
    pub async fn with_socket_path(socket_path: &str) -> Result<Self> {
        // Check if the socket exists
        if !Path::new(socket_path).exists() {
            return Err(Error::ContainerRuntime(format!(
                "Docker socket not found at {}",
                socket_path
            )));
        }

        // Create HTTP client with Unix socket support
        let client = Client::builder()
            .build(UnixConnector::default());

        Ok(Self {
            client,
            socket_path: socket_path.to_string(),
        })
    }

    /// Build a URI path for the Docker API
    fn uri_path(&self, path: &str) -> String {
        format!("/{}/{}", DOCKER_API_VERSION, path)
    }

    /// Make a request to the Docker API
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

    /// Check if Docker is available
    async fn ping(&self) -> Result<()> {
        let uri_path = self.uri_path("_ping");
        
        let req = Request::builder()
            .method(Method::GET)
            .uri(hyperlocal::Uri::new(&self.socket_path, &uri_path))
            .body(Body::empty())?;
        
        let res = self.client.request(req).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to ping Docker: {}", e)))?;
        
        if res.status().is_success() {
            Ok(())
        } else {
            Err(Error::ContainerRuntime(format!(
                "Docker ping failed with status: {}",
                res.status()
            )))
        }
    }
}

/// Docker container attach stream for reading
pub struct DockerAttachReader {
    reader: hyper::body::Body,
    buffer: Vec<u8>,
}

/// Docker container attach stream for writing
pub struct DockerAttachWriter {
    client: Client<UnixConnector, Body>,
    socket_path: String,
    container_id: String,
}

impl AsyncRead for DockerAttachReader {
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

impl AsyncWrite for DockerAttachWriter {
    fn poll_write(
        self: Pin<&mut Self>,
        _cx: &mut Context<'_>,
        buf: &[u8],
    ) -> Poll<std::io::Result<usize>> {
        // Create a request to write to the container
        let uri_path = format!("/{}/containers/{}/attach?stream=1&stdin=1", DOCKER_API_VERSION, self.container_id);
        
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
impl ContainerRuntime for DockerClient {
    async fn create_and_start_container(
        &self,
        image: &str,
        name: &str,
        command: Vec<String>,
        env_vars: HashMap<String, String>,
        labels: HashMap<String, String>,
        permission_config: ContainerPermissionConfig,
    ) -> Result<String> {
        // Ensure Docker is available
        self.ping().await?;

        // Use environment variables directly as a map

        // Convert mounts to the format Docker expects
        let mounts: Vec<DockerMount> = permission_config
            .mounts
            .iter()
            .map(|mount| DockerMount {
                r#type: "bind".to_string(),
                source: mount.source.clone(),
                target: mount.target.clone(),
                read_only: mount.read_only,
            })
            .collect();

        // Create container configuration
        let create_config = DockerCreateContainerConfig {
            image: image.to_string(),
            cmd: Some(command),
            env: Some(env_vars),
            labels: Some(labels),
            host_config: DockerHostConfig {
                mounts: Some(mounts),
                network_mode: permission_config.network_mode,
                cap_drop: Some(permission_config.cap_drop),
                cap_add: Some(permission_config.cap_add),
                security_opt: Some(permission_config.security_opt),
            },
        };

        // Create container
        let path = format!("containers/create?name={}", name);
        let create_response: DockerCreateContainerResponse = self.request(
            Method::POST,
            &path,
            Some(create_config),
        ).await?;

        // Start container
        let container_id = &create_response.id;
        let path = format!("containers/{}/start", container_id);
        let _: serde_json::Value = self.request(
            Method::POST,
            &path,
            Option::<()>::None,
        ).await?;

        Ok(container_id.clone())
    }

    async fn list_containers(&self) -> Result<Vec<ContainerInfo>> {
        // Ensure Docker is available
        self.ping().await?;

        // List containers with the vibetool label
        let path = "containers/json?filters={\"label\":[\"vibetool=true\"]}";
        let docker_containers: Vec<DockerContainer> = self.request(
            Method::GET,
            path,
            Option::<()>::None,
        ).await?;

        // Convert Docker containers to our ContainerInfo format
        let containers = docker_containers
            .into_iter()
            .map(|c| {
                let ports = c.ports.unwrap_or_default()
                    .into_iter()
                    .map(|p| PortMapping {
                        container_port: p.private_port,
                        host_port: p.public_port.unwrap_or(0),
                        protocol: p.type_,
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

        Ok(containers)
    }

    async fn stop_container(&self, container_id: &str) -> Result<()> {
        // Ensure Docker is available
        self.ping().await?;

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
        // Ensure Docker is available
        self.ping().await?;

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
        // Ensure Docker is available
        self.ping().await?;

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
        // Ensure Docker is available
        self.ping().await?;
        
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
        
        let container: DockerContainerInspect = serde_json::from_slice(&body_bytes)
            .map_err(|e| Error::Json(e))?;
        
        // Use the running boolean field directly from the container state
        // This is more reliable than checking the status string
        Ok(container.state.running)
    }

    async fn get_container_ip(&self, container_id: &str) -> Result<String> {
        // Ensure Docker is available
        self.ping().await?;

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
        
        let container: DockerContainerInspect = serde_json::from_slice(&body_bytes)
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
        // Ensure Docker is available
        self.ping().await?;

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
        
        let container: DockerContainerInspect = serde_json::from_slice(&body_bytes)
            .map_err(|e| Error::Json(e))?;

        // Convert port mappings
        let mut ports = Vec::new();
        if let Some(port_bindings) = container.network_settings.ports {
            for (port_proto, bindings) in port_bindings {
                if let Some(bindings) = bindings {
                    for binding in bindings {
                        if let Some(host_port_str) = binding.host_port {
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
        // Ensure Docker is available
        self.ping().await?;
        
        // Create the attach URL for reading
        let attach_url = format!("containers/{}/attach?stream=1&stdout=1&stderr=1", container_id);
        
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
        let reader = DockerAttachReader {
            reader: body,
            buffer: Vec::new(),
        };
        
        // Create a writer
        let writer = DockerAttachWriter {
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

// Docker API types

#[derive(Debug, Serialize)]
struct DockerCreateContainerConfig {
    #[serde(rename = "Image")]
    image: String,
    #[serde(skip_serializing_if = "Option::is_none", rename = "Cmd")]
    cmd: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "Env")]
    env: Option<HashMap<String, String>>,
    #[serde(skip_serializing_if = "Option::is_none", rename = "Labels")]
    labels: Option<HashMap<String, String>>,
    #[serde(rename = "HostConfig")]
    host_config: DockerHostConfig,
}

#[derive(Debug, Serialize)]
struct DockerHostConfig {
    #[serde(skip_serializing_if = "Option::is_none", rename = "Mounts")]
    mounts: Option<Vec<DockerMount>>,
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
struct DockerMount {
    #[serde(rename = "Type")]
    r#type: String,
    #[serde(rename = "Source")]
    source: String,
    #[serde(rename = "Target")]
    target: String,
    #[serde(rename = "ReadOnly")]
    read_only: bool,
}

#[derive(Debug, Deserialize)]
struct DockerCreateContainerResponse {
    #[serde(rename = "Id")]
    id: String,
    #[serde(default, rename = "Warnings")]
    #[allow(dead_code)]
    warnings: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct DockerContainer {
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
    ports: Option<Vec<DockerPort>>,
}

#[derive(Debug, Deserialize)]
struct DockerPort {
    #[serde(default, rename = "IP")]
    #[allow(dead_code)]
    ip: Option<String>,
    #[serde(rename = "PrivatePort")]
    private_port: u16,
    #[serde(rename = "PublicPort")]
    public_port: Option<u16>,
    #[serde(rename = "Type")]
    type_: String,
}

#[derive(Debug, Deserialize)]
struct DockerContainerInspect {
    #[serde(rename = "Id")]
    id: String,
    #[serde(rename = "Name")]
    name: String,
    #[serde(default, rename = "Created")]
    created: Option<String>,
    #[serde(rename = "State")]
    state: DockerContainerState,
    #[serde(rename = "Config")]
    config: DockerContainerConfig,
    #[serde(rename = "NetworkSettings")]
    network_settings: DockerNetworkSettings,
}

#[derive(Debug, Deserialize)]
struct DockerContainerState {
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
struct DockerContainerConfig {
    #[serde(rename = "Image")]
    image: String,
    #[serde(default, rename = "Labels")]
    labels: Option<HashMap<String, String>>,
}

#[derive(Debug, Deserialize)]
struct DockerNetworkSettings {
    #[serde(default, rename = "Ports")]
    ports: Option<HashMap<String, Option<Vec<DockerPortBinding>>>>,
    #[serde(default, rename = "Networks")]
    networks: Option<HashMap<String, DockerNetwork>>,
}

#[derive(Debug, Deserialize)]
struct DockerNetwork {
    #[serde(rename = "IPAddress")]
    ip_address: Option<String>,
    #[serde(rename = "Gateway")]
    gateway: Option<String>,
    #[serde(rename = "MacAddress")]
    mac_address: Option<String>,
}

#[derive(Debug, Deserialize)]
struct DockerPortBinding {
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
        pub DockerClient {
            fn uri_path(&self, path: &str) -> String;
            async fn ping(&self) -> Result<()>;
        }

        #[async_trait]
        impl ContainerRuntime for DockerClient {
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

    // Tests would go here, but they would require mocking the HTTP client
    // which is beyond the scope of this implementation
}