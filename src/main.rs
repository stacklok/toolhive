use std::process;

use clap::Parser;
use tokio::runtime::Runtime;

use mcp_lok::cli::{Cli, Commands};
use mcp_lok::error::Result;

/// Main entry point
fn main() {
    // Create a tokio runtime
    let rt = Runtime::new().expect("Failed to create tokio runtime");

    // Run the async main function
    let result = rt.block_on(async_main());

    // Handle errors
    if let Err(e) = result {
        eprintln!("Error: {}", e);
        process::exit(1);
    }
}

/// Async main function
async fn async_main() -> Result<()> {
    // Parse command line arguments
    let cli = Cli::parse();

    // Execute the appropriate command
    match cli.command {
        Some(Commands::Run(args)) => args.execute().await,
        Some(Commands::List(args)) => args.execute().await,
        Some(Commands::Start(args)) => args.execute().await,
        Some(Commands::Stop(args)) => args.execute().await,
        Some(Commands::Remove(args)) => args.execute().await,
        None => {
            println!("No command specified. Use --help for usage information.");
            Ok(())
        }
    }
}
