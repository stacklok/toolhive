use clap::Args;

use crate::container::ContainerRuntimeFactory;
use crate::error::Result;

/// Stop an MCP server
#[derive(Args, Debug)]
pub struct StopCommand {
    /// Name or ID of the MCP server to stop
    pub name_or_id: String,
}

impl StopCommand {
    /// Run the command
    pub async fn execute(&self) -> Result<()> {
        // Create container runtime
        let runtime = ContainerRuntimeFactory::create().await?;

        // List containers to find the one with the given name or ID
        let containers = runtime.list_containers().await?;

        // Find the container by name or ID
        let container = containers
            .iter()
            .find(|c| c.id.starts_with(&self.name_or_id) || c.name == self.name_or_id)
            .ok_or_else(|| {
                crate::error::Error::ContainerNotFound(self.name_or_id.clone())
            })?;

        // Stop the container
        runtime.stop_container(&container.id).await?;

        println!("MCP server {} stopped", container.name);

        Ok(())
    }
}