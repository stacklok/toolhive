use clap::Args;
use std::collections::HashMap;
use std::path::PathBuf;

use crate::container::ContainerRuntimeFactory;
use crate::error::Result;
use crate::permissions::profile::PermissionProfile;
use crate::transport::{TransportFactory, TransportMode};

/// Start an MCP server in the background
#[derive(Args, Debug)]
pub struct StartCommand {
    /// Transport mode (sse or stdio)
    #[arg(long, default_value = "sse")]
    pub transport: String,

    /// Name of the MCP server
    #[arg(long)]
    pub name: String,

    /// Port to expose (for SSE transport)
    #[arg(long)]
    pub port: Option<u16>,

    /// Permission profile to use (stdio, network, or path to JSON file)
    #[arg(long, default_value = "stdio")]
    pub permission_profile: String,

    /// Image to use for the MCP server
    pub image: String,

    /// Arguments to pass to the MCP server
    #[arg(last = true)]
    pub args: Vec<String>,
}

impl StartCommand {
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

        // Validate port for SSE transport
        let port = match transport_mode {
            TransportMode::SSE => {
                self.port.ok_or_else(|| {
                    crate::error::Error::InvalidArgument(
                        "Port is required for SSE transport".to_string(),
                    )
                })?
            }
            _ => self.port.unwrap_or(0),
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
        let mut runtime = ContainerRuntimeFactory::create().await?;

        // Create labels for the container
        let mut labels = HashMap::new();
        labels.insert("mcp-lok".to_string(), "true".to_string());
        labels.insert("mcp-lok-name".to_string(), self.name.clone());
        labels.insert("mcp-lok-transport".to_string(), self.transport.clone());

        // Create environment variables for the container
        let mut env_vars = HashMap::new();

        // Create transport handler
        let transport = TransportFactory::create(transport_mode, port);
        
        // If using stdio transport, set the runtime
        let transport = match transport_mode {
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

        // Set up the transport
        transport.setup("", &self.name, self.port, &mut env_vars).await?;

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

        // Start the transport
        transport.setup(&container_id, &self.name, self.port, &mut HashMap::new()).await?;
        transport.start().await?;

        println!("MCP server {} started with container ID {}", self.name, container_id);

        Ok(())
    }
}