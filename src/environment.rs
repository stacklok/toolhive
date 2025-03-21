use crate::error::{Error, Result};
use crate::transport::TransportMode;
use std::collections::HashMap;

/// Parse environment variables from CLI input
///
/// This function takes a vector of strings in the format "KEY=VALUE" and
/// parses them into a HashMap of environment variables.
pub fn parse_environment_variables(env_vars_input: &[String]) -> Result<HashMap<String, String>> {
    let mut env_vars = HashMap::new();

    for env_var in env_vars_input {
        if let Some(pos) = env_var.find('=') {
            let key = env_var[..pos].to_string();
            let value = env_var[pos + 1..].to_string();
            env_vars.insert(key, value);
        } else {
            return Err(Error::InvalidArgument(format!(
                "Invalid environment variable format: {}. Expected format: KEY=VALUE",
                env_var
            )));
        }
    }

    Ok(env_vars)
}

/// Set environment variables for MCP servers based on transport mode and port
pub fn set_transport_environment_variables(
    env_vars: &mut HashMap<String, String>,
    transport_mode: &TransportMode,
    port: u16,
) {
    // Set common transport environment variables
    env_vars.insert(
        "MCP_TRANSPORT".to_string(),
        transport_mode.as_str().to_string(),
    );
    env_vars.insert("MCP_PORT".to_string(), port.to_string());

    // Set transport-specific environment variables
    match transport_mode {
        TransportMode::SSE => {
            // Additional environment variables for SSE transport
            env_vars.insert("PORT".to_string(), port.to_string());
            env_vars.insert("MCP_SSE_ENABLED".to_string(), "true".to_string());
        }
        TransportMode::STDIO => {
            // No additional environment variables for STDIO transport
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_parse_environment_variables() {
        // Test valid environment variables
        let env_vars_input = vec!["KEY1=value1".to_string(), "KEY2=value2".to_string()];
        let result = parse_environment_variables(&env_vars_input);

        assert!(result.is_ok());
        let env_vars = result.unwrap();
        assert_eq!(env_vars.get("KEY1").unwrap(), "value1");
        assert_eq!(env_vars.get("KEY2").unwrap(), "value2");

        // Test invalid environment variable
        let invalid_env_var = vec!["INVALID_ENV_VAR".to_string()];
        let result = parse_environment_variables(&invalid_env_var);

        assert!(result.is_err());
        if let Err(Error::InvalidArgument(msg)) = result {
            assert!(msg.contains("Invalid environment variable format"));
        } else {
            panic!("Expected InvalidArgument error");
        }
    }

    #[test]
    fn test_set_transport_environment_variables_stdio() {
        let mut env_vars = HashMap::new();
        set_transport_environment_variables(&mut env_vars, &TransportMode::STDIO, 8080);

        assert_eq!(env_vars.get("MCP_TRANSPORT").unwrap(), "stdio");
        assert_eq!(env_vars.get("MCP_PORT").unwrap(), "8080");
        assert!(!env_vars.contains_key("PORT"));
        assert!(!env_vars.contains_key("MCP_SSE_ENABLED"));
    }

    #[test]
    fn test_set_transport_environment_variables_sse() {
        let mut env_vars = HashMap::new();
        set_transport_environment_variables(&mut env_vars, &TransportMode::SSE, 9000);

        assert_eq!(env_vars.get("MCP_TRANSPORT").unwrap(), "sse");
        assert_eq!(env_vars.get("MCP_PORT").unwrap(), "9000");
        assert_eq!(env_vars.get("PORT").unwrap(), "9000");
        assert_eq!(env_vars.get("MCP_SSE_ENABLED").unwrap(), "true");
    }
}
