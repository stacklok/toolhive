use cucumber::{then, when};
use regex::Regex;

use crate::McpLokWorld;
use crate::common::utils::execute_mcp_lok;
use crate::common::assertions::{assert_command_failure, assert_output_contains};

#[when(expr = "I run the {string} command")]
fn run_command(world: &mut McpLokWorld, command: String) {
    // Build the command arguments
    let args: Vec<&str> = vec![&command];
    
    println!("Executing command: cargo run -- {}", command);
    
    // Execute the command
    let output = execute_mcp_lok(&args).expect("Failed to execute mcp-lok command");
    
    // Store the command output
    world.command_output = Some(output);
    
    // Print the command output for debugging
    if let Some(ref output) = world.command_output {
        println!("Command stdout: {}", String::from_utf8_lossy(&output.stdout));
        println!("Command stderr: {}", String::from_utf8_lossy(&output.stderr));
    }
}

#[when(expr = "I run the command with arguments {string}")]
fn run_command_with_args(world: &mut McpLokWorld, args_str: String) {
    // Split the arguments string by whitespace
    let args: Vec<&str> = args_str.split_whitespace().collect();
    
    println!("Executing command: cargo run -- {}", args_str);
    
    // Execute the command
    let output = execute_mcp_lok(&args).expect("Failed to execute mcp-lok command");
    
    // Store the command output
    world.command_output = Some(output);
    
    // Print the command output for debugging
    if let Some(ref output) = world.command_output {
        println!("Command stdout: {}", String::from_utf8_lossy(&output.stdout));
        println!("Command stderr: {}", String::from_utf8_lossy(&output.stderr));
    }
}

#[then(expr = "the output should contain {string}")]
fn output_contains(world: &mut McpLokWorld, expected: String) {
    if let Some(ref output) = world.command_output {
        assert_output_contains(output, &expected).expect(&format!("Output does not contain '{}'", expected));
    } else {
        panic!("No command output available");
    }
}

#[then("the output should contain the version number")]
fn output_contains_version(world: &mut McpLokWorld) {
    if let Some(ref output) = world.command_output {
        let stdout = String::from_utf8_lossy(&output.stdout);
        let stderr = String::from_utf8_lossy(&output.stderr);
        
        // Check if the output contains a version number pattern (e.g., 0.1.0)
        let version_pattern = r"\d+\.\d+\.\d+";
        let re = Regex::new(version_pattern).unwrap();
        
        assert!(
            re.is_match(&stdout) || re.is_match(&stderr),
            "Expected output to contain a version number (e.g., 0.1.0), but got:\nSTDOUT: {}\nSTDERR: {}",
            stdout, stderr
        );
    } else {
        panic!("No command output available");
    }
}

#[then("the command should fail")]
fn command_should_fail(world: &mut McpLokWorld) {
    if let Some(ref output) = world.command_output {
        assert_command_failure(output).expect("Command succeeded, but was expected to fail");
    } else {
        panic!("No command output available");
    }
}

#[then("the output should contain an error message")]
fn output_contains_error(world: &mut McpLokWorld) {
    if let Some(ref output) = world.command_output {
        let stdout = String::from_utf8_lossy(&output.stdout);
        let stderr = String::from_utf8_lossy(&output.stderr);
        
        // Check if the output contains common error terms
        let error_terms = ["error", "invalid", "unknown", "not found", "failed"];
        
        let contains_error = error_terms.iter().any(|term| {
            stdout.to_lowercase().contains(term) || stderr.to_lowercase().contains(term)
        });
        
        assert!(
            contains_error,
            "Expected output to contain an error message, but got:\nSTDOUT: {}\nSTDERR: {}",
            stdout, stderr
        );
    } else {
        panic!("No command output available");
    }
}