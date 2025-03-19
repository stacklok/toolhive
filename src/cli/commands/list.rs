use clap::Args;
use serde::Serialize;

use crate::container::{ContainerRuntime, ContainerRuntimeFactory};
use crate::error::Result;
use crate::labels;

/// List running MCP servers
#[derive(Args, Debug)]
pub struct ListCommand {
    /// Show all containers (default shows just running)
    #[arg(short, long)]
    pub all: bool,

    /// Output format (json or text)
    #[arg(long, default_value = "text")]
    pub format: String,
}

/// Container information for JSON output
#[derive(Serialize)]
struct ContainerOutput {
    id: String,
    name: String,
    image: String,
    state: String,
    transport: String,
    port: u16,
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

        match self.format.as_str() {
            "json" => self.print_json_output(&containers)?,
            _ => self.print_text_output(&containers),
        }

        Ok(())
    }

    /// Print container information in JSON format
    fn print_json_output(&self, containers: &[crate::container::ContainerInfo]) -> Result<()> {
        let output: Vec<ContainerOutput> = containers.iter().map(|container| {
            // Truncate container ID to first 12 characters (similar to Docker)
            let truncated_id = container.id.chars().take(12).collect::<String>();
            
            // Get transport and port from labels
            let transport = labels::get_transport(&container.labels);
            let port = labels::get_port(&container.labels);
            
            ContainerOutput {
                id: truncated_id,
                name: container.name.clone(),
                image: container.image.clone(),
                state: container.state.clone(),
                transport: transport.to_string(),
                port,
            }
        }).collect();
        
        println!("{}", serde_json::to_string_pretty(&output)?);
        Ok(())
    }

    /// Print container information in text format
    fn print_text_output(&self, containers: &[crate::container::ContainerInfo]) {
        println!("{:<12} {:<20} {:<40} {:<15} {:<10} {:<6}", "CONTAINER ID", "NAME", "IMAGE", "STATE", "TRANSPORT", "PORT");
        for container in containers {
            // Truncate container ID to first 12 characters (similar to Docker)
            let truncated_id = container.id.chars().take(12).collect::<String>();
            
            // Get transport and port from labels
            let transport = labels::get_transport(&container.labels);
            let port = labels::get_port(&container.labels);
            
            println!(
                "{:<12} {:<20} {:<40} {:<15} {:<10} {:<6}",
                truncated_id, container.name, container.image, container.state, transport, port
            );
        }
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
        let cmd = ListCommand { all: true, format: "text".to_string() };
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
        let cmd = ListCommand { all: false, format: "text".to_string() };
        let result = cmd.execute_with_runtime(Box::new(mock_runtime)).await;
        
        // Verify the result
        assert!(result.is_ok());
        
        Ok(())
    }

    #[tokio::test]
    async fn test_list_command_json_output() -> Result<()> {
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
        
        // Create and execute the command with JSON output
        let cmd = ListCommand { all: true, format: "json".to_string() };
        let result = cmd.execute_with_runtime(Box::new(mock_runtime)).await;
        
        // Verify the result
        assert!(result.is_ok());
        
        Ok(())
    }
}