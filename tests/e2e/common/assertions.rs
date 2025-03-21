use std::process::Output;
use std::str;

/// Asserts that a command output contains a specific string
pub fn assert_output_contains(output: &Output, expected: &str) -> Result<(), String> {
    let stdout = str::from_utf8(&output.stdout).unwrap_or("");
    let stderr = str::from_utf8(&output.stderr).unwrap_or("");

    if stdout.contains(expected) || stderr.contains(expected) {
        Ok(())
    } else {
        Err(format!(
            "Expected output to contain '{}', but got:\nSTDOUT: {}\nSTDERR: {}",
            expected, stdout, stderr
        ))
    }
}

/// Asserts that a command was successful (exit code 0)
pub fn assert_command_success(output: &Output) -> Result<(), String> {
    if output.status.success() {
        Ok(())
    } else {
        let stderr = str::from_utf8(&output.stderr).unwrap_or("");
        Err(format!(
            "Command failed with exit code {:?}:\nSTDERR: {}",
            output.status.code(),
            stderr
        ))
    }
}

/// Asserts that a command failed (non-zero exit code)
pub fn assert_command_failure(output: &Output) -> Result<(), String> {
    if !output.status.success() {
        Ok(())
    } else {
        Err("Command succeeded, but was expected to fail".to_string())
    }
}
