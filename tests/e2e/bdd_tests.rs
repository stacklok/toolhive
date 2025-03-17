use cucumber::{cli::Args, writer, World as _};
use std::path::PathBuf;

// Import our cucumber world and steps
mod common;
mod steps;

// Define our cucumber world
#[derive(cucumber::World, Debug, Default)]
pub struct McpLokWorld {
    // State shared between steps
    pub command_output: Option<std::process::Output>,
    pub container_id: Option<String>,
    pub server_name: Option<String>,
    pub transport_type: Option<String>,
    pub port: Option<u16>,
    pub image_name: Option<String>,
    pub error_message: Option<String>,
    pub temp_dir: Option<PathBuf>,
}

// Main function that runs our tests
fn main() {
    // Set up logging
    let _ = env_logger::builder().is_test(true).try_init();

    // Create a new cucumber runner
    McpLokWorld::cucumber()
        .with_writer(writer::Basic::new(
            std::io::stdout(),
            writer::Coloring::Auto,
            writer::Verbosity::Default,
        ))
        .run("tests/e2e/features");
}