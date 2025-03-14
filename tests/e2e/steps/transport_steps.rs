use cucumber::{then, when};

use crate::McpLokWorld;
use crate::common::utils::{execute_mcp_lok, extract_container_id, create_container_runtime_command};

#[when(expr = "I start an MCP server with name {string} and transport {string} and port {int}")]
fn start_mcp_server_with_port(world: &mut McpLokWorld, name: String, transport: String, port: u16) {
    // Store the server name, transport, and port for later use
    world.server_name = Some(name.clone());
    world.transport_type = Some(transport.clone());
    world.port = Some(port);
    
    // Get the image name
    let image_name = world.image_name.clone().unwrap_or_else(|| "localhost/basic-mcp-server".to_string());
    
    // Convert port to string
    let port_str = port.to_string();
    
    // Build the command arguments
    let args = vec![
        "start",
        "--name", &name,
        "--transport", &transport,
        "--port", &port_str,
        &image_name,
    ];
    
    println!("Executing command: cargo run -- {}", args.join(" "));
    
    // Execute the command
    let output = execute_mcp_lok(&args).expect("Failed to execute mcp-lok start command");
    
    // Store the command output
    world.command_output = Some(output);
    
    // Print the command output for debugging
    if let Some(ref output) = world.command_output {
        println!("Command stdout: {}", String::from_utf8_lossy(&output.stdout));
        println!("Command stderr: {}", String::from_utf8_lossy(&output.stderr));
    }
    
    // Extract the container ID from the output
    if let Some(ref output) = world.command_output {
        let stdout = String::from_utf8_lossy(&output.stdout);
        world.container_id = extract_container_id(&stdout);
        println!("Extracted container ID: {:?}", world.container_id);
    }
}

#[then("I should be able to connect to the SSE endpoint")]
fn connect_to_sse_endpoint(world: &mut McpLokWorld) {
    if let Some(port) = world.port {
        // Use curl to check if the SSE endpoint is accessible
        let mut cmd = create_container_runtime_command("curl");
        
        let output = cmd
            .args(["-v", "-N", &format!("http://localhost:{}", port)])
            .output()
            .expect("Failed to execute curl command");
        
        println!("Curl stdout: {}", String::from_utf8_lossy(&output.stdout));
        println!("Curl stderr: {}", String::from_utf8_lossy(&output.stderr));
        
        // Check if the connection was successful
        let stderr = String::from_utf8_lossy(&output.stderr);
        assert!(
            stderr.contains("Connected to localhost") || stderr.contains("200 OK"),
            "Failed to connect to SSE endpoint: {}",
            stderr
        );
    } else {
        panic!("No port available");
    }
}