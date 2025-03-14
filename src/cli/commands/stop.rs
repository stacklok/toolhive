use clap::Args;

use crate::container::{ContainerRuntime, ContainerRuntimeFactory};
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
        
        // Execute with the runtime
        self.execute_with_runtime(runtime).await
    }
    
    /// Run the command with a specific runtime (for testing)
    pub async fn execute_with_runtime(&self, runtime: Box<dyn ContainerRuntime>) -> Result<()> {
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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cli::commands::tests::{create_mock_runtime, create_test_container_info};
    use mockall::predicate::*;
    
    #[tokio::test]
    async fn test_stop_command() -> Result<()> {
        // Create a mock runtime
        let mut mock_runtime = create_mock_runtime();
        
        // Set up expectations for list_containers
        mock_runtime.expect_list_containers()
            .times(1)
            .returning(|| {
                Ok(vec![
                    create_test_container_info("container1", "test-container-1", "Up 10 minutes"),
                ])
            });
            
        // Set up expectations for stop_container
        mock_runtime.expect_stop_container()
            .with(eq("container1"))
            .times(1)
            .returning(|_| Ok(()));
        
        // Create and execute the command
        let cmd = StopCommand {
            name_or_id: "test-container-1".to_string(),
        };
        let result = cmd.execute_with_runtime(Box::new(mock_runtime)).await;
        
        // Verify the result
        assert!(result.is_ok());
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_stop_command_container_not_found() -> Result<()> {
        // Create a mock runtime
        let mut mock_runtime = create_mock_runtime();
        
        // Set up expectations for list_containers
        mock_runtime.expect_list_containers()
            .times(1)
            .returning(|| {
                Ok(vec![
                    create_test_container_info("container1", "test-container-1", "Up 10 minutes"),
                ])
            });
        
        // Create and execute the command
        let cmd = StopCommand {
            name_or_id: "non-existent-container".to_string(),
        };
        let result = cmd.execute_with_runtime(Box::new(mock_runtime)).await;
        
        // Verify the result is an error
        assert!(result.is_err());
        
        Ok(())
    }
}