mod cli;
mod container;
mod permissions;

use anyhow::{Context, Result};
use std::path::PathBuf;
use tracing::info;

#[tokio::main]
async fn main() {
    // Initialize logging with more verbose output
    tracing_subscriber::fmt()
        .with_max_level(tracing::Level::DEBUG)
        .with_env_filter("mcp_lok=debug")
        .init();

    // Parse command-line arguments
    let cli = cli::parse_args();

    // Handle commands
    let result = match cli.command {
        Some(cli::Commands::Run {
            name,
            transport,
            port,
            permission_profile,
            image,
            args,
        }) => {
            run_server(&name, &transport, port, permission_profile, &image, &args, false).await
        }
        Some(cli::Commands::List) => {
            list_servers().await
        }
        Some(cli::Commands::Start {
            name,
            transport,
            port,
            permission_profile,
            image,
            args,
        }) => {
            run_server(&name, &transport, port, permission_profile, &image, &args, true).await
        }
        Some(cli::Commands::Stop { name }) => {
            stop_server(&name).await
        }
        Some(cli::Commands::Rm { name }) => {
            remove_server(&name).await
        }
        Some(cli::Commands::Version) => {
            println!("mcp-lok version {}", env!("CARGO_PKG_VERSION"));
            Ok(())
        }
        None => {
            // Default command: start an MCP server that manages mcp-lok servers
            println!("Starting MCP server manager...");
            // TODO: Implement MCP server manager
            println!("Not implemented yet");
            Ok(())
        }
    };

    // Handle errors
    if let Err(err) = result {
        eprintln!("Error: {}", err);
        
        // Print cause chain for better debugging
        let mut source = err.source();
        while let Some(err) = source {
            eprintln!("\nCaused by:\n    {}", err);
            source = err.source();
        }
        
        // Provide helpful hints based on error type
        if err.to_string().contains("socket") {
            eprintln!("\nHint: Make sure Docker or Podman is installed and running.");
            eprintln!("You may need to start the Docker/Podman service or check permissions.");
        }
        
        std::process::exit(1);
    }
}

/// Run an MCP server
async fn run_server(
    name: &str,
    transport_str: &str,
    port: Option<u16>,
    permission_profile_path: Option<PathBuf>,
    image: &str,
    args: &[String],
    detach: bool,
) -> Result<()> {
    // Parse transport mode
    let transport = container::TransportMode::from_str(transport_str)
        .context("Invalid transport mode")?;

    // Load permission profile
    let profile = if let Some(path) = permission_profile_path {
        permissions::PermissionProfile::from_file(path)?
    } else {
        // Use default profile based on transport
        match transport {
            container::TransportMode::SSE => permissions::PermissionProfile::network_profile(),
            container::TransportMode::STDIO => permissions::PermissionProfile::stdio_profile(),
        }
    };

    // Create container manager
    let manager = container::ContainerManager::new().await?;

    // Run container
    let container_id = manager
        .run_container(name, image, transport, port, &profile, args, detach)
        .await?;

    info!("Started MCP server: {}", name);
    info!("Container ID: {}", container_id);

    Ok(())
}

/// List running MCP servers
async fn list_servers() -> Result<()> {
    // Create container manager
    let manager = container::ContainerManager::new().await?;

    // List containers
    let containers = manager.list_containers().await?;

    if containers.is_empty() {
        println!("No MCP servers running");
    } else {
        println!("Running MCP servers:");
        for (name, id) in containers {
            println!("  {} ({})", name, id);
        }
    }

    Ok(())
}

/// Stop an MCP server
async fn stop_server(name: &str) -> Result<()> {
    // Create container manager
    let manager = container::ContainerManager::new().await?;

    // Stop container
    manager.stop_container(name).await?;

    info!("Stopped MCP server: {}", name);

    Ok(())
}

/// Remove an MCP server
async fn remove_server(name: &str) -> Result<()> {
    // Create container manager
    let manager = container::ContainerManager::new().await?;

    // Remove container
    manager.remove_container(name).await?;

    info!("Removed MCP server: {}", name);

    Ok(())
}
