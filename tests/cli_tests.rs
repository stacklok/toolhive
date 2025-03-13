use clap::Parser;
use mcp_lok::cli::{Cli, Commands};
use std::path::PathBuf;

#[test]
fn test_run_command() {
    let args = vec![
        "mcp-lok",
        "run",
        "--name",
        "test-server",
        "--transport",
        "sse",
        "--port",
        "8080",
        "my-image:latest",
        "--",
        "arg1",
        "arg2",
    ];

    let cli = Cli::parse_from(args);
    
    match cli.command {
        Some(Commands::Run {
            name,
            transport,
            port,
            permission_profile,
            image,
            args,
        }) => {
            assert_eq!(name, "test-server");
            assert_eq!(transport, "sse");
            assert_eq!(port, Some(8080));
            assert_eq!(permission_profile, None);
            assert_eq!(image, "my-image:latest");
            assert_eq!(args, vec!["arg1", "arg2"]);
        }
        _ => panic!("Expected Run command"),
    }
}

#[test]
fn test_list_command() {
    let args = vec!["mcp-lok", "list"];
    
    let cli = Cli::parse_from(args);
    
    match cli.command {
        Some(Commands::List) => {}
        _ => panic!("Expected List command"),
    }
}

#[test]
fn test_stop_command() {
    let args = vec!["mcp-lok", "stop", "test-server"];
    
    let cli = Cli::parse_from(args);
    
    match cli.command {
        Some(Commands::Stop { name }) => {
            assert_eq!(name, "test-server");
        }
        _ => panic!("Expected Stop command"),
    }
}

#[test]
fn test_rm_command() {
    let args = vec!["mcp-lok", "rm", "test-server"];
    
    let cli = Cli::parse_from(args);
    
    match cli.command {
        Some(Commands::Rm { name }) => {
            assert_eq!(name, "test-server");
        }
        _ => panic!("Expected Rm command"),
    }
}

#[test]
fn test_version_command() {
    let args = vec!["mcp-lok", "version"];
    
    let cli = Cli::parse_from(args);
    
    match cli.command {
        Some(Commands::Version) => {}
        _ => panic!("Expected Version command"),
    }
}

#[test]
fn test_permission_profile() {
    let profile_path = PathBuf::from("/path/to/profile.json");
    let args = vec![
        "mcp-lok",
        "run",
        "--name",
        "test-server",
        "--transport",
        "stdio",
        "--permission-profile",
        profile_path.to_str().unwrap(),
        "my-image:latest",
    ];

    let cli = Cli::parse_from(args);
    
    match cli.command {
        Some(Commands::Run {
            name,
            transport,
            port,
            permission_profile,
            image,
            args,
        }) => {
            assert_eq!(name, "test-server");
            assert_eq!(transport, "stdio");
            assert_eq!(port, None);
            assert_eq!(permission_profile, Some(profile_path));
            assert_eq!(image, "my-image:latest");
            assert_eq!(args, Vec::<String>::new());
        }
        _ => panic!("Expected Run command"),
    }
}