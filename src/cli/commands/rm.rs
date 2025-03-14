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

#[cfg(test)]
mod tests {
    use super::*;
    use crate::cli::commands::tests::{create_mock_runtime, create_test_container_info};
    use mockall::predicate::*;
    
    #[tokio::test]
    async fn test_remove_command_not_running() -> Result<()> {
        // Create a mock runtime
        let mut mock_runtime = create_mock_runtime();
        
        // Set up expectations for list_containers
        mock_runtime.expect_list_containers()
            .times(1)
            .returning(|| {
                Ok(vec![
                    create_test_container_info("container1", "test-container-1", "Exited (0) 5 minutes ago"),
                ])
            });
            
        // Set up expectations for is_container_running
        mock_runtime.expect_is_container_running()
            .with(eq("container1"))
            .times(1)
            .returning(|_| Ok(false));
            
        // Set up expectations for remove_container
        mock_runtime.expect_remove_container()
            .with(eq("container1"))
            .times(1)
            .returning(|_| Ok(()));
        
        // Create and execute the command
        let cmd = RemoveCommand {
            name_or_id: "test-container-1".to_string(),
            force: false,
        };
        let result = cmd.execute_with_runtime(Box::new(mock_runtime)).await;
        
        // Verify the result
        assert!(result.is_ok());
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_remove_command_running_with_force() -> Result<()> {
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
            
        // Set up expectations for is_container_running
        mock_runtime.expect_is_container_running()
            .with(eq("container1"))
            .times(1)
            .returning(|_| Ok(true));
            
        // Set up expectations for stop_container
        mock_runtime.expect_stop_container()
            .with(eq("container1"))
            .times(1)
            .returning(|_| Ok(()));
            
        // Set up expectations for remove_container
        mock_runtime.expect_remove_container()
            .with(eq("container1"))
            .times(1)
            .returning(|_| Ok(()));
        
        // Create and execute the command
        let cmd = RemoveCommand {
            name_or_id: "test-container-1".to_string(),
            force: true,
        };
        let result = cmd.execute_with_runtime(Box::new(mock_runtime)).await;
        
        // Verify the result
        assert!(result.is_ok());
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_remove_command_running_without_force() -> Result<()> {
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
            
        // Set up expectations for is_container_running
        mock_runtime.expect_is_container_running()
            .with(eq("container1"))
            .times(1)
            .returning(|_| Ok(true));
        
        // Create and execute the command
        let cmd = RemoveCommand {
            name_or_id: "test-container-1".to_string(),
            force: false,
        };
        let result = cmd.execute_with_runtime(Box::new(mock_runtime)).await;
        
        // Verify the result is an error
        assert!(result.is_err());
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_remove_command_container_not_found() -> Result<()> {
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
        let cmd = RemoveCommand {
            name_or_id: "non-existent-container".to_string(),
            force: false,
        };
        let result = cmd.execute_with_runtime(Box::new(mock_runtime)).await;
        
        // Verify the result is an error
        assert!(result.is_err());
        
        Ok(())
    }
}