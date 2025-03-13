use clap::{Parser, Subcommand};

pub mod commands;

/// MCP-LOK: A lightweight, secure, and fast manager for MCP servers
#[derive(Parser, Debug)]
#[command(author, version, about, long_about = None)]
pub struct Cli {
    #[command(subcommand)]
    pub command: Option<Commands>,
}

/// Commands for MCP-LOK
#[derive(Subcommand, Debug)]
pub enum Commands {
    /// Run an MCP server
    #[command(name = "run")]
    Run(commands::run::RunCommand),

    /// List running MCP servers
    #[command(name = "list")]
    List(commands::list::ListCommand),

    /// Start an MCP server in the background
    #[command(name = "start")]
    Start(commands::start::StartCommand),

    /// Stop an MCP server
    #[command(name = "stop")]
    Stop(commands::stop::StopCommand),

    /// Remove an MCP server
    #[command(name = "rm")]
    Remove(commands::rm::RemoveCommand),
}