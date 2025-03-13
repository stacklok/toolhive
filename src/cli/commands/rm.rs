use clap::Args;

use crate::container::{ContainerRuntime, ContainerRuntimeFactory};
use crate::error::Result;

/// Remove an MCP server
#[derive(Args, Debug)]
pub struct RemoveCommand {
    /// Name or ID of the MCP server to remove
    pub name_or_id: String,

    /// Force removal of a running container
    #[arg(short, long)]
    pub force: bool,
}

impl RemoveCommand {
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

        // Check if the container is running
        let is_running = runtime.is_container_running(&container.id).await?;

        // If the container is running and force is not specified, return an error
        if is_running && !self.force {
            return Err(crate::error::Error::ContainerRuntime(format!(
                "Container {} is running. Use --force to remove it",
                container.name
            )));
        }

        // Stop the container if it's running
        if is_running {
            runtime.stop_container(&container.id).await?;
        }

        // Remove the container
        runtime.remove_container(&container.id).await?;

        println!("MCP server {} removed", container.name);

        Ok(())
    }
}