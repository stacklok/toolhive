use std::process;

use clap::Parser;
use env_logger::Builder;
use log::{error, LevelFilter};
use tokio::runtime::Runtime;

use vibetool::cli::{Cli, Commands};
use vibetool::error::Result;

/// Main entry point
fn main() {
    // Parse command line arguments
    let cli = Cli::parse();
    
    // Initialize logger based on debug flag
    let mut builder = Builder::new();
    builder.filter_level(if cli.debug {
        LevelFilter::Debug
    } else {
        LevelFilter::Info
    });
    builder.init();
    
    // Create a tokio runtime
    let rt = Runtime::new().expect("Failed to create tokio runtime");

    // Run the async main function
    let result = rt.block_on(async_main(cli));

    // Handle errors
    if let Err(e) = result {
        error!("Error: {}", e);
        process::exit(1);
    }
}

/// Async main function
async fn async_main(cli: Cli) -> Result<()> {
    // Execute the appropriate command
    match cli.command {
        Some(Commands::Run(args)) => args.execute().await,
        Some(Commands::List(args)) => args.execute().await,
        Some(Commands::Start(args)) => args.execute().await,
        Some(Commands::Stop(args)) => args.execute().await,
        Some(Commands::Remove(args)) => args.execute().await,
        None => {
            log::info!("No command specified. Use --help for usage information.");
            Ok(())
        }
    }
}
