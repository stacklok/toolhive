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
use tokio::io::{AsyncRead, AsyncWrite, ReadBuf};

use crate::container::{ContainerInfo, ContainerRuntime, PortMapping};
use crate::error::{Error, Result};
use crate::permissions::profile::ContainerPermissionConfig;

// Podman socket paths
const PODMAN_SOCKET_PATH: &str = "/var/run/podman/podman.sock";
const PODMAN_XDG_RUNTIME_SOCKET_PATH: &str = "podman/podman.sock";
const PODMAN_API_VERSION: &str = "v4.0.0";

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

    /// Create a new Podman client with a custom socket path
    pub async fn with_socket_path(socket_path: &str) -> Result<Self> {
        // Check if the socket exists
        if !Path::new(socket_path).exists() {
            return Err(Error::ContainerRuntime(format!(
                "Podman socket not found at {}",
                socket_path
            )));
        }

        // Create HTTP client with Unix socket support
        let client = Client::builder()
            .build(UnixConnector::default());

        let podman = Self {
            client,
            socket_path: socket_path.to_string(),
        };

        // Verify that Podman is available
        podman.ping().await?;

        Ok(podman)
    }

    /// Find the Podman socket path
    fn find_podman_socket() -> Result<String> {
        // Check standard location
        if Path::new(PODMAN_SOCKET_PATH).exists() {
            return Ok(PODMAN_SOCKET_PATH.to_string());
        }

        // Check XDG_RUNTIME_DIR location
        if let Ok(xdg_runtime_dir) = env::var("XDG_RUNTIME_DIR") {
            let xdg_socket_path = PathBuf::from(xdg_runtime_dir)
                .join(PODMAN_XDG_RUNTIME_SOCKET_PATH);
            
            if xdg_socket_path.exists() {
                return Ok(xdg_socket_path.to_string_lossy().to_string());
            }
        }

        // Check user-specific location
        if let Ok(home) = env::var("HOME") {
            let user_socket_path = PathBuf::from(home)
                .join(".local/share/containers/podman/machine/podman.sock");
            
            if user_socket_path.exists() {
                return Ok(user_socket_path.to_string_lossy().to_string());
            }
        }

        Err(Error::ContainerRuntime("Podman socket not found".to_string()))
    }

    /// Build a URI path for the Podman API
    fn uri_path(&self, path: &str) -> String {
        format!("/libpod/{}/{}", PODMAN_API_VERSION, path)
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
        let uri_path = self.uri_path("_ping");
        
        let req = Request::builder()
            .method(Method::GET)
            .uri(hyperlocal::Uri::new(&self.socket_path, &uri_path))
            .body(Body::empty())?;
        
        let res = self.client.request(req).await
            .map_err(|e| Error::ContainerRuntime(format!("Failed to ping Podman: {}", e)))?;
        
        if res.status().is_success() {
            Ok(())
        } else {
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
        // Create a request to write to the container
        let uri_path = format!("/libpod/{}/containers/{}/attach?stream=1&stdin=1", PODMAN_API_VERSION, self.container_id);
        
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
    async fn create_and_start_container(
        &self,
        image: &str,
        name: &str,
        command: Vec<String>,
        env_vars: HashMap<String, String>,
        labels: HashMap<String, String>,
        permission_config: ContainerPermissionConfig,
    ) -> Result<String> {
        // Convert environment variables to the format Podman expects
        let env: Vec<String> = env_vars
            .iter()
            .map(|(k, v)| format!("{}={}", k, v))
            .collect();

        // Convert mounts to the format Podman expects
        let mounts: Vec<PodmanMount> = permission_config
            .mounts
            .iter()
            .map(|mount| PodmanMount {
                Type: "bind".to_string(),
                Source: mount.source.clone(),
                Destination: mount.target.clone(),
                Options: if mount.read_only {
                    Some(vec!["ro".to_string()])
                } else {
                    None
                },
            })
            .collect();

        // Create container configuration
        let create_config = PodmanCreateContainerConfig {
            Image: image.to_string(),
            Command: Some(command),
            Env: Some(env),
            Labels: Some(labels),
            HostConfig: PodmanHostConfig {
                Mounts: Some(mounts),
                NetworkMode: permission_config.network_mode,
                CapDrop: Some(permission_config.cap_drop),
                CapAdd: Some(permission_config.cap_add),
                SecurityOpt: Some(permission_config.security_opt),
            },
        };

        // Create container
        let path = format!("containers/create?name={}", name);
        let create_response: PodmanCreateContainerResponse = self.request(
            Method::POST,
            &path,
            Some(create_config),
        ).await?;

        // Start container
        let container_id = &create_response.Id;
        let path = format!("containers/{}/start", container_id);
        let _: serde_json::Value = self.request(
            Method::POST,
            &path,
            Option::<()>::None,
        ).await?;

        Ok(container_id.clone())
    }

    async fn list_containers(&self) -> Result<Vec<ContainerInfo>> {
        // List containers with the mcp-lok label
        let path = "containers/json?filters={\"label\":[\"mcp-lok=true\"]}";
        let podman_containers: Vec<PodmanContainer> = self.request(
            Method::GET,
            path,
            Option::<()>::None,
        ).await?;

        // Convert Podman containers to our ContainerInfo format
        let containers = podman_containers
            .into_iter()
            .map(|c| {
                let ports = c.Ports.unwrap_or_default()
                    .into_iter()
                    .map(|p| PortMapping {
                        container_port: p.container_port,
                        host_port: p.host_port.unwrap_or(0),
                        protocol: p.protocol,
                    })
                    .collect();

                ContainerInfo {
                    id: c.Id,
                    name: c.Names.unwrap_or_default().first().cloned().unwrap_or_default(),
                    image: c.Image,
                    status: c.Status,
                    created: c.Created,
                    labels: c.Labels.unwrap_or_default(),
                    ports,
                }
            })
            .collect();

        Ok(containers)
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
        let container_info = self.get_container_info(container_id).await?;
        
        // Check if the container is running
        Ok(container_info.status.contains("Up"))
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
        if let Some(port_bindings) = &container.NetworkSettings.Ports {
            for (port_proto, bindings) in port_bindings {
                if let Some(bindings) = bindings {
                    for binding in bindings {
                        if let Some(host_port_str) = &binding.HostPort {
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

        Ok(ContainerInfo {
            id: container.Id,
            name: container.Name,
            image: container.Config.Image,
            status: container.State.Status,
            created: container.Created,
            labels: container.Config.Labels.unwrap_or_default(),
            ports,
        })
    }
    
    async fn attach_container(&self, container_id: &str) -> Result<(Box<dyn AsyncWrite + Unpin + Send>, Box<dyn AsyncRead + Unpin + Send>)> {
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
    Image: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    Command: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    Env: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    Labels: Option<HashMap<String, String>>,
    HostConfig: PodmanHostConfig,
}

#[derive(Debug, Serialize)]
struct PodmanHostConfig {
    #[serde(skip_serializing_if = "Option::is_none")]
    Mounts: Option<Vec<PodmanMount>>,
    NetworkMode: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    CapDrop: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    CapAdd: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    SecurityOpt: Option<Vec<String>>,
}

#[derive(Debug, Serialize)]
struct PodmanMount {
    Type: String,
    Source: String,
    Destination: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    Options: Option<Vec<String>>,
}

#[derive(Debug, Deserialize)]
struct PodmanCreateContainerResponse {
    Id: String,
    #[serde(default)]
    Warnings: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct PodmanContainer {
    Id: String,
    #[serde(default)]
    Names: Option<Vec<String>>,
    Image: String,
    #[serde(default)]
    ImageID: String,
    Status: String,
    Created: u64,
    #[serde(default)]
    Labels: Option<HashMap<String, String>>,
    #[serde(default)]
    Ports: Option<Vec<PodmanPort>>,
}

#[derive(Debug, Deserialize)]
struct PodmanPort {
    #[serde(default)]
    host_ip: Option<String>,
    container_port: u16,
    host_port: Option<u16>,
    protocol: String,
}

#[derive(Debug, Deserialize)]
struct PodmanContainerInspect {
    Id: String,
    Name: String,
    Created: u64,
    State: PodmanContainerState,
    Config: PodmanContainerConfig,
    NetworkSettings: PodmanNetworkSettings,
}

#[derive(Debug, Deserialize)]
struct PodmanContainerState {
    Status: String,
    Running: bool,
    Paused: bool,
    Restarting: bool,
    OOMKilled: bool,
    Dead: bool,
    Pid: i64,
    ExitCode: i64,
    Error: String,
    StartedAt: String,
    FinishedAt: String,
}

#[derive(Debug, Deserialize)]
struct PodmanContainerConfig {
    Image: String,
    #[serde(default)]
    Labels: Option<HashMap<String, String>>,
}

#[derive(Debug, Deserialize)]
struct PodmanNetworkSettings {
    #[serde(default)]
    Ports: Option<HashMap<String, Option<Vec<PodmanPortBinding>>>>,
}

#[derive(Debug, Deserialize)]
struct PodmanPortBinding {
    HostIp: Option<String>,
    HostPort: Option<String>,
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

    // Tests would go here, but they would require mocking the HTTP client
    // which is beyond the scope of this implementation
}