use clap::Args;
use std::collections::HashMap;
use std::path::PathBuf;

use crate::container::{ContainerMonitor, ContainerRuntime, ContainerRuntimeFactory};
use crate::error::Result;
use crate::permissions::profile::PermissionProfile;
use crate::transport::{Transport, TransportFactory, TransportMode};

/// Run an MCP server
#[derive(Args, Debug)]
pub struct RunCommand {
    /// Transport mode (sse or stdio)
    #[arg(long, default_value = "sse")]
    pub transport: String,

    /// Name of the MCP server
    #[arg(long)]
    pub name: String,

    /// Port to expose (for SSE transport or STDIO reverse proxy)
    #[arg(long)]
    pub port: Option<u16>,

    /// Permission profile to use (stdio, network, or path to JSON file)
    #[arg(long, default_value = "stdio")]
    pub permission_profile: String,

    /// Environment variables to pass to the MCP server (format: KEY=VALUE)
    #[arg(long, short = 'e')]
    pub env: Vec<String>,

    /// Image to use for the MCP server
    pub image: String,

    /// Arguments to pass to the MCP server
    #[arg(last = true)]
    pub args: Vec<String>,
}

impl RunCommand {
    /// Run the command
    pub async fn execute(&self) -> Result<()> {
        // Parse transport mode
        let transport_mode = TransportMode::from_str(&self.transport)
            .ok_or_else(|| {
                crate::error::Error::InvalidArgument(format!(
                    "Invalid transport mode: {}. Valid modes are: sse, stdio",
                    self.transport
                ))
            })?;

        // Validate port for SSE transport (required) and get port for STDIO transport (optional)
        let port = match transport_mode {
            TransportMode::SSE => {
                self.port.ok_or_else(|| {
                    crate::error::Error::InvalidArgument(
                        "Port is required for SSE transport".to_string(),
                    )
                })?
            }
            TransportMode::STDIO => {
                // Port is optional for STDIO transport (used for reverse proxy)
                self.port.unwrap_or(0)
            }
        };

        // Load permission profile
        let permission_profile = match self.permission_profile.as_str() {
            "stdio" => PermissionProfile::builtin_stdio_profile(),
            "network" => PermissionProfile::builtin_network_profile(),
            path => PermissionProfile::from_file(&PathBuf::from(path))?,
        };

        // Convert permission profile to container config
        let permission_config = permission_profile.to_container_config()?;

        // Create container runtime
        let runtime = ContainerRuntimeFactory::create().await?;
        
        // Create transport handler
        let transport = TransportFactory::create(transport_mode, port);
        
        // Execute with the runtime and transport
        self.execute_with_runtime_and_transport(runtime, transport, permission_config, false).await
    }
    
    /// Run the command with a specific runtime and transport (for testing)
    pub async fn execute_with_runtime_and_transport(
        &self,
        mut runtime: Box<dyn ContainerRuntime>,
        transport: Box<dyn Transport>,
        permission_config: crate::permissions::profile::ContainerPermissionConfig,
        skip_ctrl_c: bool, // For testing
    ) -> Result<()> {
        // Create labels for the container
        let mut labels = HashMap::new();
        labels.insert("mcp-lok".to_string(), "true".to_string());
        labels.insert("mcp-lok-name".to_string(), self.name.clone());
        labels.insert("mcp-lok-transport".to_string(), self.transport.clone());

        // Create environment variables for the container
        let mut env_vars = HashMap::new();
        
        // Add user-provided environment variables
        for env_var in &self.env {
            if let Some(pos) = env_var.find('=') {
                let key = env_var[..pos].to_string();
                let value = env_var[pos + 1..].to_string();
                env_vars.insert(key, value);
            } else {
                return Err(crate::error::Error::InvalidArgument(format!(
                    "Invalid environment variable format: {}. Expected format: KEY=VALUE",
                    env_var
                )));
            }
        }

        // If using stdio transport, set the runtime
        let transport = match transport.mode() {
            TransportMode::STDIO => {
                let stdio_transport = transport.as_any().downcast_ref::<crate::transport::stdio::StdioTransport>()
                    .ok_or_else(|| crate::error::Error::Transport("Failed to downcast to StdioTransport".to_string()))?;
                
                // Clone the transport and set the runtime
                let stdio_transport = stdio_transport.clone().with_runtime(runtime);
                
                // Get a new runtime instance
                runtime = ContainerRuntimeFactory::create().await?;
                
                // Box the transport
                Box::new(stdio_transport) as Box<dyn crate::transport::Transport>
            },
            _ => transport,
        };

        // Create and start the container
        let container_id = runtime
            .create_and_start_container(
                &self.image,
                &self.name,
                self.args.clone(),
                env_vars,
                labels,
                permission_config,
            )
            .await?;

        // Get the container IP address
        println!("Getting container IP address for {}", container_id);
        let container_ip = match runtime.get_container_ip(&container_id).await {
            Ok(ip) => {
                println!("Container IP address: {}", ip);
                Some(ip)
            },
            Err(e) => {
                eprintln!("Failed to get container IP address: {}", e);
                None
            }
        };

        // Start the transport
        let mut transport_env_vars = HashMap::new();
        transport.setup(&container_id, &self.name, self.port, &mut transport_env_vars, container_ip).await?;
        transport.start().await?;

        println!("MCP server {} started with container ID {}", self.name, container_id);
        
        // Create a container monitor
        let runtime_for_monitor = ContainerRuntimeFactory::create().await?;
        let mut monitor = ContainerMonitor::new(runtime_for_monitor, &container_id, &self.name);
        
        // Start monitoring the container
        let mut error_rx = monitor.start_monitoring().await?;
        
        if !skip_ctrl_c {
            println!("Press Ctrl+C to stop or wait for container to exit");

            // Create a future that completes when Ctrl+C is pressed
            let ctrl_c = tokio::signal::ctrl_c();
            
            tokio::select! {
                // Wait for Ctrl+C
                _ = ctrl_c => {
                    println!("Received Ctrl+C, stopping MCP server...");
                },
                // Wait for container exit error
                Some(err) = error_rx.recv() => {
                    eprintln!("Container exited unexpectedly: {}", err);
                }
            }

            // Stop monitoring
            monitor.stop_monitoring().await;
            
            // Stop the transport
            let _ = transport.stop().await;

            // Try to stop the container (it might already be stopped)
            let _ = runtime.stop_container(&container_id).await;

            println!("MCP server {} stopped", self.name);
        }

        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    
    #[tokio::test]
    async fn test_run_command_env_vars() -> Result<()> {
        // Test valid environment variables
        let env_vars = vec!["KEY1=value1".to_string(), "KEY2=value2".to_string()];
        let mut result_map = HashMap::new();
        
        for env_var in &env_vars {
            if let Some(pos) = env_var.find('=') {
                let key = env_var[..pos].to_string();
                let value = env_var[pos + 1..].to_string();
                result_map.insert(key, value);
            }
        }
        
        assert_eq!(result_map.get("KEY1").unwrap(), "value1");
        assert_eq!(result_map.get("KEY2").unwrap(), "value2");
        
        // Test invalid environment variable
        let invalid_env_var = "INVALID_ENV_VAR".to_string();
        let pos = invalid_env_var.find('=');
        
        assert!(pos.is_none());
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_run_command_transport_validation() -> Result<()> {
        // Test SSE transport without port
        let cmd = RunCommand {
            transport: "sse".to_string(),
            name: "test-server".to_string(),
            port: None, // Missing port
            permission_profile: "network".to_string(),
            env: vec![],
            image: "test-image".to_string(),
            args: vec![],
        };
        
        // Parse transport mode
        let transport_mode = TransportMode::from_str(&cmd.transport).unwrap();
        
        // Validate port for SSE transport
        let result = match transport_mode {
            TransportMode::SSE => {
                cmd.port.ok_or_else(|| {
                    crate::error::Error::InvalidArgument(
                        "Port is required for SSE transport".to_string(),
                    )
                })
            }
            _ => Ok(cmd.port.unwrap_or(0)),
        };
        
        // Verify the result is an error
        assert!(result.is_err());
        
        // Test with valid port
        let cmd = RunCommand {
            transport: "sse".to_string(),
            name: "test-server".to_string(),
            port: Some(8080), // Valid port
            permission_profile: "network".to_string(),
            env: vec![],
            image: "test-image".to_string(),
            args: vec![],
        };
        
        // Parse transport mode
        let transport_mode = TransportMode::from_str(&cmd.transport).unwrap();
        
        // Validate port for SSE transport
        let result = match transport_mode {
            TransportMode::SSE => {
                cmd.port.ok_or_else(|| {
                    crate::error::Error::InvalidArgument(
                        "Port is required for SSE transport".to_string(),
                    )
                })
            }
            _ => Ok(cmd.port.unwrap_or(0)),
        };
        
        // Verify the result is ok
        assert!(result.is_ok());
        
        // Test invalid transport mode
        let cmd = RunCommand {
            transport: "invalid".to_string(),
            name: "test-server".to_string(),
            port: Some(8080),
            permission_profile: "network".to_string(),
            env: vec![],
            image: "test-image".to_string(),
            args: vec![],
        };
        
        // Parse transport mode
        let result = TransportMode::from_str(&cmd.transport)
            .ok_or_else(|| {
                crate::error::Error::InvalidArgument(format!(
                    "Invalid transport mode: {}. Valid modes are: sse, stdio",
                    cmd.transport
                ))
            });
        
        // Verify the result is an error
        assert!(result.is_err());
        
        Ok(())
    }
}