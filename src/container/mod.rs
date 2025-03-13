//! Container module for mcp-lok
//!
//! This module handles container operations using the Docker/Podman API.

use anyhow::{Context, Result};
use bollard::container::{
    Config, CreateContainerOptions, ListContainersOptions, RemoveContainerOptions,
    StartContainerOptions, StopContainerOptions,
};
use bollard::models::{HostConfig, Mount, MountTypeEnum, PortBinding};
use bollard::Docker;
use std::collections::HashMap;
use tracing::{debug, info};

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
        // Try to connect to Docker socket first, then Podman if that fails
        let docker = match Docker::connect_with_socket_defaults() {
            Ok(docker) => docker,
            Err(_) => {
                // Try Podman socket as fallback
                Docker::connect_with_socket_defaults()
                    .context("Failed to connect to Docker or Podman socket")?
            }
        };

        Ok(ContainerManager { docker })
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
        let host_config = self.create_host_config(transport, port, profile)?;

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

        Ok(response.id)
    }

    /// Create host config based on transport mode and permissions
    fn create_host_config(
        &self,
        transport: TransportMode,
        port: Option<u16>,
        profile: &PermissionProfile,
    ) -> Result<HostConfig> {
        let mut host_config = HostConfig::default();

        // Set up mounts
        let mut mounts = Vec::new();

        // Add socket mount for STDIO transport
        if transport == TransportMode::STDIO {
            mounts.push(Mount {
                target: Some("/var/run/mcp.sock".to_string()),
                source: Some("/var/run/mcp.sock".to_string()),
                typ: Some(MountTypeEnum::BIND),
                read_only: Some(false),
                ..Default::default()
            });
        }

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