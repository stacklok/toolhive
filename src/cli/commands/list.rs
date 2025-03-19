use clap::Args;

use crate::container::{ContainerRuntime, ContainerRuntimeFactory};
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
        
        // Execute with the runtime
        self.execute_with_runtime(runtime).await
    }
    
    /// Run the command with a specific runtime (for testing)
    pub async fn execute_with_runtime(&self, runtime: Box<dyn ContainerRuntime>) -> Result<()> {
        // List containers
        let containers = runtime.list_containers().await?;

        // Filter containers if not showing all
        let containers = if !self.all {
            containers
                .into_iter()
                .filter(|c| c.state.contains("running"))
                .collect()
        } else {
            containers
        };

        // Print container information
        println!("{:<20} {:<20} {:<40} {:<20}", "CONTAINER ID", "NAME", "IMAGE", "STATE");
        for container in containers {
            println!(
                "{:<20} {:<20} {:<40} {:<20}",
                container.id, container.name, container.image, container.state
            );
        }

        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cli::commands::tests::{create_mock_runtime, create_test_container_info};
    
    #[tokio::test]
    async fn test_list_command_all_containers() -> Result<()> {
        // Create a mock runtime
        let mut mock_runtime = create_mock_runtime();
        
        // Set up expectations for list_containers
        mock_runtime.expect_list_containers()
            .times(1)
            .returning(|| {
                Ok(vec![
                    create_test_container_info("container1", "test-container-1", "Up 10 minutes"),
                    create_test_container_info("container2", "test-container-2", "Exited (0) 5 minutes ago"),
                ])
            });
        
        // Create and execute the command
        let cmd = ListCommand { all: true };
        let result = cmd.execute_with_runtime(Box::new(mock_runtime)).await;
        
        // Verify the result
        assert!(result.is_ok());
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_list_command_running_containers() -> Result<()> {
        // Create a mock runtime
        let mut mock_runtime = create_mock_runtime();
        
        // Set up expectations for list_containers
        mock_runtime.expect_list_containers()
            .times(1)
            .returning(|| {
                Ok(vec![
                    create_test_container_info("container1", "test-container-1", "Up 10 minutes"),
                    create_test_container_info("container2", "test-container-2", "Exited (0) 5 minutes ago"),
                ])
            });
        
        // Create and execute the command
        let cmd = ListCommand { all: false };
        let result = cmd.execute_with_runtime(Box::new(mock_runtime)).await;
        
        // Verify the result
        assert!(result.is_ok());
        
        Ok(())
    }
}