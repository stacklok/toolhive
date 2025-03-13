//! CLI module for mcp-lok
//! 
//! This module handles the command-line interface for mcp-lok.

use clap::{Parser, Subcommand};
use std::path::PathBuf;

/// mcp-lok is a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers.
#[derive(Parser, Debug)]
#[clap(author, version, about, long_about = None)]
pub struct Cli {
    #[clap(subcommand)]
    pub command: Option<Commands>,
}

/// Supported subcommands
#[derive(Subcommand, Debug)]
pub enum Commands {
    /// Run an MCP server
    Run {
        /// Name of the MCP server
        #[clap(long)]
        name: String,

        /// Transport mode (sse or stdio)
        #[clap(long)]
        transport: String,

        /// Port to expose (for SSE transport)
        #[clap(long)]
        port: Option<u16>,

        /// Path to permission profile JSON file
        #[clap(long)]
        permission_profile: Option<PathBuf>,

        /// Image to run
        image: String,

        /// Arguments to pass to the container
        #[clap(last = true)]
        args: Vec<String>,
    },

    /// List running MCP servers
    List,

    /// Start an MCP server and send it to the background
    Start {
        /// Name of the MCP server
        #[clap(long)]
        name: String,

        /// Transport mode (sse or stdio)
        #[clap(long)]
        transport: String,

        /// Port to expose (for SSE transport)
        #[clap(long)]
        port: Option<u16>,

        /// Path to permission profile JSON file
        #[clap(long)]
        permission_profile: Option<PathBuf>,

        /// Image to run
        image: String,

        /// Arguments to pass to the container
        #[clap(last = true)]
        args: Vec<String>,
    },

    /// Stop an MCP server
    Stop {
        /// Name of the MCP server to stop
        name: String,
    },

    /// Remove an MCP server
    Rm {
        /// Name of the MCP server to remove
        name: String,
    },

    /// Show the current version of mcp-lok
    Version,
}

/// Parse command-line arguments
pub fn parse_args() -> Cli {
    Cli::parse()
}