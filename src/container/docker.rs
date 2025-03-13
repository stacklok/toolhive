use async_trait::async_trait;
use hyper::{Body, Client, Method, Request, StatusCode};
use hyper::body::HttpBody;
use hyperlocal::UnixConnector;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::path::Path;
use std::pin::Pin;
use std::task::{Context, Poll};
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

        // Convert environment variables to the format Docker expects
        let env: Vec<String> = env_vars
            .iter()
            .map(|(k, v)| format!("{}={}", k, v))
            .collect();

        // Convert mounts to the format Docker expects
        let mounts: Vec<DockerMount> = permission_config
            .mounts
            .iter()
            .map(|mount| DockerMount {
                Type: "bind".to_string(),
                Source: mount.source.clone(),
                Target: mount.target.clone(),
                ReadOnly: mount.read_only,
            })
            .collect();

        // Create container configuration
        let create_config = DockerCreateContainerConfig {
            Image: image.to_string(),
            Cmd: Some(command),
            Env: Some(env),
            Labels: Some(labels),
            HostConfig: DockerHostConfig {
                Mounts: Some(mounts),
                NetworkMode: permission_config.network_mode,
                CapDrop: Some(permission_config.cap_drop),
                CapAdd: Some(permission_config.cap_add),
                SecurityOpt: Some(permission_config.security_opt),
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
        // Ensure Docker is available
        self.ping().await?;

        // List containers with the mcp-lok label
        let path = "containers/json?filters={\"label\":[\"mcp-lok=true\"]}";
        let docker_containers: Vec<DockerContainer> = self.request(
            Method::GET,
            path,
            Option::<()>::None,
        ).await?;

        // Convert Docker containers to our ContainerInfo format
        let containers = docker_containers
            .into_iter()
            .map(|c| {
                let ports = c.Ports.unwrap_or_default()
                    .into_iter()
                    .map(|p| PortMapping {
                        container_port: p.PrivatePort,
                        host_port: p.PublicPort.unwrap_or(0),
                        protocol: p.Type,
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
        // Get container info
        let container_info = self.get_container_info(container_id).await?;
        
        // Check if the container is running
        Ok(container_info.status.contains("Up"))
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
        if let Some(port_bindings) = container.NetworkSettings.Ports {
            for (port_proto, bindings) in port_bindings {
                if let Some(bindings) = bindings {
                    for binding in bindings {
                        if let Some(host_port_str) = binding.HostPort {
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
    Image: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    Cmd: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    Env: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    Labels: Option<HashMap<String, String>>,
    HostConfig: DockerHostConfig,
}

#[derive(Debug, Serialize)]
struct DockerHostConfig {
    #[serde(skip_serializing_if = "Option::is_none")]
    Mounts: Option<Vec<DockerMount>>,
    NetworkMode: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    CapDrop: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    CapAdd: Option<Vec<String>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    SecurityOpt: Option<Vec<String>>,
}

#[derive(Debug, Serialize)]
struct DockerMount {
    #[serde(rename = "Type")]
    Type: String,
    #[serde(rename = "Source")]
    Source: String,
    #[serde(rename = "Target")]
    Target: String,
    #[serde(rename = "ReadOnly")]
    ReadOnly: bool,
}

#[derive(Debug, Deserialize)]
struct DockerCreateContainerResponse {
    Id: String,
    #[serde(default)]
    Warnings: Vec<String>,
}

#[derive(Debug, Deserialize)]
struct DockerContainer {
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
    Ports: Option<Vec<DockerPort>>,
}

#[derive(Debug, Deserialize)]
struct DockerPort {
    #[serde(default)]
    IP: Option<String>,
    PrivatePort: u16,
    PublicPort: Option<u16>,
    Type: String,
}

#[derive(Debug, Deserialize)]
struct DockerContainerInspect {
    Id: String,
    Name: String,
    Created: u64,
    State: DockerContainerState,
    Config: DockerContainerConfig,
    NetworkSettings: DockerNetworkSettings,
}

#[derive(Debug, Deserialize)]
struct DockerContainerState {
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
struct DockerContainerConfig {
    Image: String,
    #[serde(default)]
    Labels: Option<HashMap<String, String>>,
}

#[derive(Debug, Deserialize)]
struct DockerNetworkSettings {
    #[serde(default)]
    Ports: Option<HashMap<String, Option<Vec<DockerPortBinding>>>>,
}

#[derive(Debug, Deserialize)]
struct DockerPortBinding {
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
            async fn attach_container(&self, container_id: &str) -> Result<(Box<dyn AsyncWrite + Unpin + Send>, Box<dyn AsyncRead + Unpin + Send>)>;
        }
    }

    // Tests would go here, but they would require mocking the HTTP client
    // which is beyond the scope of this implementation
}