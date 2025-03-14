use std::process::{Command, Output};
use std::str;
use std::env;

/// Determines which container runtime to use (podman or docker)
pub fn get_container_runtime() -> String {
    env::var("CONTAINER_RUNTIME").unwrap_or_else(|_| "podman".to_string())
}

/// Executes mcp-lok command
pub fn execute_mcp_lok(args: &[&str]) -> std::io::Result<Output> {
    Command::new("cargo")
        .arg("run")
        .arg("--")
        .args(args)
        .output()
}

/// Extracts container ID from command output
pub fn extract_container_id(output: &str) -> Option<String> {
    // Example pattern: "MCP server started with container ID: abcdef123456"
    let lines: Vec<&str> = output.lines().collect();
    for line in lines {
        if line.contains("container ID") {
            return line.split_whitespace()
                .last()
                .map(|s| s.trim().to_string());
        }
    }
    None
}

/// Creates a Command with the appropriate environment variables for container runtime
pub fn create_container_runtime_command(cmd: &str) -> Command {
    let mut command = Command::new(cmd);
    
    // Pass through DOCKER_HOST and CONTAINER_HOST environment variables if they exist
    if let Ok(docker_host) = env::var("DOCKER_HOST") {
        println!("Using DOCKER_HOST: {}", docker_host);
        command.env("DOCKER_HOST", docker_host);
    }
    
    if let Ok(container_host) = env::var("CONTAINER_HOST") {
        println!("Using CONTAINER_HOST: {}", container_host);
        command.env("CONTAINER_HOST", container_host);
    }
    
    command
}

/// Executes a container runtime command with the appropriate environment variables
pub fn execute_container_runtime(args: &[&str]) -> std::io::Result<Output> {
    let runtime = get_container_runtime();
    let mut command = create_container_runtime_command(&runtime);
    command.args(args).output()
}

/// Checks if a container is running
pub fn is_container_running(container_id: &str) -> std::io::Result<bool> {
    let output = execute_container_runtime(&["ps", "--filter", &format!("id={}", container_id), "--format", "{{.ID}}"])?;
    
    let output_str = str::from_utf8(&output.stdout).unwrap_or("");
    Ok(!output_str.trim().is_empty())
}

/// Lists all running containers
pub fn list_containers() -> std::io::Result<Output> {
    execute_container_runtime(&["ps"])
}

/// Executes a command inside a container
pub fn exec_in_container(container_id: &str, cmd: &[&str]) -> std::io::Result<Output> {
    let mut args = vec!["exec", container_id];
    args.extend_from_slice(cmd);
    execute_container_runtime(&args)
}

/// Cleans up a container if it exists
pub fn cleanup_container(container_id: &str) -> std::io::Result<()> {
    // Try to stop the container if it's running
    let _ = execute_container_runtime(&["stop", container_id]);
    
    // Try to remove the container
    let _ = execute_container_runtime(&["rm", "-f", container_id]);
    
    Ok(())
}

/// Extracts the number of containers from the list command output
pub fn extract_container_count(output: &str) -> Option<i32> {
    for line in output.lines() {
        if line.starts_with("Found ") && line.contains("containers") {
            let parts: Vec<&str> = line.split_whitespace().collect();
            if parts.len() >= 2 {
                if let Ok(count) = parts[1].parse::<i32>() {
                    return Some(count);
                }
            }
        }
    }
    None
}