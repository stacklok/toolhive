use clap::Args;

use crate::container::ContainerRuntimeFactory;
use crate::error::Result;

/// List running MCP servers
#[derive(Args, Debug)]
pub struct ListCommand {
    /// Show all containers (default shows just running)
    #[arg(short, long)]
    pub all: bool,
}

impl ListCommand {
    /// Run the command
    pub async fn execute(&self) -> Result<()> {
        // Create container runtime
        let runtime = ContainerRuntimeFactory::create().await?;

        // List containers
        let containers = runtime.list_containers().await?;

        // Filter containers if not showing all
        let containers = if !self.all {
            containers
                .into_iter()
                .filter(|c| c.status.contains("Up"))
                .collect()
        } else {
            containers
        };

        // Print container information
        println!("{:<20} {:<20} {:<40} {:<20}", "CONTAINER ID", "NAME", "IMAGE", "STATUS");
        for container in containers {
            println!(
                "{:<20} {:<20} {:<40} {:<20}",
                container.id, container.name, container.image, container.status
            );
        }

        Ok(())
    }
}