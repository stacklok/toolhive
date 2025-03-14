# End-to-End Test Plan for mcp-lok

## 1. Introduction

### 1.1 Purpose
This document outlines the end-to-end (E2E) testing strategy for mcp-lok, a lightweight, secure, and fast manager for MCP (Model Context Protocol) servers. The E2E tests will verify that the entire system functions correctly from a user's perspective, ensuring that all components work together as expected.

### 1.2 Scope
The E2E tests will cover:
- CLI command functionality
- Container lifecycle management
- Transport mechanisms (SSE and stdio)
- Permission profiles and security constraints
- MCP protocol communication

### 1.3 Objectives
- Validate the complete workflow of mcp-lok from CLI input to container execution
- Ensure proper isolation and security of MCP servers
- Verify correct implementation of the MCP protocol
- Detect regressions in functionality during development

## 2. Test Environment

### 2.1 Requirements
- Linux environment with Podman/Docker installed
- Rust development environment
- Network access for testing SSE transport
- Sample MCP servers for testing

### 2.2 Setup
```bash
# Clone the repository
git clone https://github.com/stacklok/mcp-lok.git
cd mcp-lok

# Build the project
cargo build --release

# Build test MCP servers
cd sample-mcp-servers
./build-and-run.sh build
cd ..

# Set up test directory
mkdir -p tests/e2e
```

## 3. Test Categories and Scenarios

### 3.1 CLI Command Tests

#### 3.1.1 Basic Command Validation
- **Objective**: Verify all CLI commands produce expected output
- **Scenarios**:
  - Test help command
  - Test version command
  - Test invalid commands/arguments

#### 3.1.2 Server Lifecycle Commands
- **Objective**: Verify server start, list, stop, and remove commands
- **Scenarios**:
  - Start an MCP server and verify it's running
  - List running MCP servers
  - Stop an MCP server and verify it's stopped
  - Remove an MCP server and verify it's removed

### 3.2 Container Management Tests

#### 3.2.1 Container Creation
- **Objective**: Verify containers are created with correct configuration
- **Scenarios**:
  - Create container with default settings
  - Create container with custom name
  - Create container with custom port

#### 3.2.2 Container Runtime Integration
- **Objective**: Verify proper integration with container runtimes
- **Scenarios**:
  - Test with Docker runtime
  - Test with Podman runtime
  - Test container resource limits

### 3.3 Transport Mechanism Tests

#### 3.3.1 SSE Transport
- **Objective**: Verify SSE transport works correctly
- **Scenarios**:
  - Start server with SSE transport
  - Send requests to SSE endpoint
  - Verify responses follow SSE protocol
  - Test error handling

#### 3.3.2 stdio Transport
- **Objective**: Verify stdio transport works correctly
- **Scenarios**:
  - Start server with stdio transport
  - Send requests via stdin
  - Verify responses via stdout
  - Test error handling

### 3.4 Permission Profile Tests

#### 3.4.1 Built-in Profiles
- **Objective**: Verify built-in permission profiles work correctly
- **Scenarios**:
  - Test stdio profile (no network access)
  - Test network profile (outbound network access)

#### 3.4.2 Custom Profiles
- **Objective**: Verify custom permission profiles work correctly
- **Scenarios**:
  - Create and use custom profile with specific read/write permissions
  - Create and use custom profile with specific network permissions
  - Test invalid permission profiles

### 3.5 MCP Protocol Tests

#### 3.5.1 Protocol Compliance
- **Objective**: Verify MCP protocol implementation is correct
- **Scenarios**:
  - Test initialization sequence
  - Test resource listing and reading
  - Test tool listing and calling
  - Test error handling

#### 3.5.2 Protocol Edge Cases
- **Objective**: Verify handling of edge cases in the protocol
- **Scenarios**:
  - Test large messages
  - Test malformed messages
  - Test protocol version negotiation

## 4. Test Implementation Approaches

### 4.1 Rust-native Testing with assert_cmd and predicates

```rust
// Example implementation for CLI command testing
use assert_cmd::Command;
use predicates::prelude::*;

#[test]
fn test_start_stop_lifecycle() {
    // Start an MCP server
    let mut cmd = Command::cargo_bin("mcp-lok").unwrap();
    let assert = cmd
        .args(["start", "--transport", "sse", "--name", "test-server", "--port", "8080", 
               "sample-mcp-servers/basic-mcp-server"])
        .assert();
    
    assert.success()
          .stdout(predicate::str::contains("MCP server test-server started"));
    
    // Get the container ID from the output
    let output = String::from_utf8(assert.get_output().stdout.clone()).unwrap();
    let container_id = extract_container_id(&output);
    
    // List running servers
    let mut cmd = Command::cargo_bin("mcp-lok").unwrap();
    cmd.arg("list")
       .assert()
       .success()
       .stdout(predicate::str::contains("test-server"));
    
    // Stop the server
    let mut cmd = Command::cargo_bin("mcp-lok").unwrap();
    cmd.args(["stop", &container_id])
       .assert()
       .success();
    
    // Verify it's stopped
    let mut cmd = Command::cargo_bin("mcp-lok").unwrap();
    cmd.arg("list")
       .assert()
       .success()
       .stdout(predicate::str::contains("test-server").not());
}
```

### 4.2 HTTP Client Testing with reqwest

```rust
// Example implementation for SSE transport testing
#[tokio::test]
async fn test_sse_transport_e2e() -> Result<()> {
    // Start an MCP server with SSE transport
    let output = Command::new("podman")
        .args(["run", "-u", "jaosorior", "-w", "/var/home/jaosorior/Development/stacklok/mcp-lok-1", 
              "dev", "cargo", "run", "--", "start", "test-server", "--transport", "sse", 
              "--port", "8080", "--name", "test-sse", "sample-mcp-servers/basic-mcp-server"])
        .output()?;
    
    // Extract container ID
    let output_str = String::from_utf8_lossy(&output.stdout);
    let container_id = extract_container_id(&output_str)?;
    
    // Test HTTP interaction
    let client = reqwest::Client::new();
    let response = client.post("http://localhost:8080")
        .header("Content-Type", "text/event-stream")
        .body("event: initialize\ndata: {\"clientInfo\":{\"name\":\"test-client\"}}\nid: 1\n\n")
        .send()
        .await?;
    
    assert!(response.status().is_success());
    
    // Clean up
    Command::new("podman")
        .args(["run", "-u", "jaosorior", "-w", "/var/home/jaosorior/Development/stacklok/mcp-lok-1", 
              "dev", "cargo", "run", "--", "stop", &container_id])
        .output()?;
    
    Ok(())
}
```

### 4.3 BDD-style Testing with cucumber-rs

```rust
// features/server_lifecycle.feature
Feature: MCP Server Lifecycle
  Scenario: Starting and stopping an MCP server
    Given I have a valid MCP server image
    When I start an MCP server with name "test-server" and transport "sse"
    Then the server should be running
    When I stop the server
    Then the server should not be running

// steps/server_steps.rs
use cucumber::{given, when, then};

#[given("I have a valid MCP server image")]
async fn valid_server_image(world: &mut MyWorld) {
    // Setup: Ensure the test image exists
    let output = Command::new("podman")
        .args(["images", "test-mcp-server"])
        .output()
        .await
        .expect("Failed to execute command");
    
    // If image doesn't exist, build it
    if !String::from_utf8_lossy(&output.stdout).contains("test-mcp-server") {
        Command::new("podman")
            .args(["build", "-t", "test-mcp-server", "./sample-mcp-servers/basic-mcp-server"])
            .output()
            .await
            .expect("Failed to build test image");
    }
    
    world.image_name = "test-mcp-server".to_string();
}
```

### 4.4 Integration with testcontainers-rs

```rust
// Example implementation for container testing
use testcontainers::*;

#[test]
fn test_container_permissions() {
    let docker = clients::Cli::default();
    
    // Start a test container
    let container = docker.run(images::generic::GenericImage::new("sample-mcp-servers/basic-mcp-server"));
    
    // Use mcp-lok to manage it
    // ...
    
    // Verify permissions are correctly applied
    // ...
}
```

## 5. Test Execution Strategy

### 5.1 Local Development Testing
- Run subset of E2E tests during development
- Use `cargo test --test e2e` to run E2E tests separately from unit tests
- Implement test fixtures that clean up after themselves

### 5.2 CI/CD Integration
- Run full E2E test suite on pull requests
- Run E2E tests on multiple platforms (Linux variants)
- Set up test reporting and artifact collection

### 5.3 Test Data Management
- Create dedicated test MCP servers with predictable behavior
- Use temporary directories for test artifacts
- Implement proper cleanup of containers and resources

## 6. Test Reporting and Monitoring

### 6.1 Test Reports
- Generate JUnit XML reports for CI integration
- Capture logs for failed tests
- Record test execution times

### 6.2 Test Metrics
- Track test coverage
- Monitor test execution time trends
- Track flaky tests

## 7. Maintenance Considerations

### 7.1 Test Code Organization
```
tests/
├── e2e/
│   ├── cli_tests.rs
│   ├── container_tests.rs
│   ├── transport_tests.rs
│   ├── permission_tests.rs
│   └── protocol_tests.rs
├── fixtures/
│   ├── test_servers/
│   └── permission_profiles/
└── common/
    ├── utils.rs
    └── assertions.rs
```

### 7.2 Best Practices
- Keep tests independent and idempotent
- Implement proper setup and teardown
- Use descriptive test names
- Add comments explaining test purpose and approach
- Avoid hardcoded values, use constants or configuration

### 7.3 Continuous Improvement
- Regularly review and update test plan
- Add new tests for new features
- Refactor tests as needed to improve maintainability
- Monitor and address flaky tests

## 8. Implementation Timeline

### Phase 1: Basic CLI and Container Tests
- Set up E2E test infrastructure
- Implement basic CLI command tests
- Implement container lifecycle tests

### Phase 2: Transport and Protocol Tests
- Implement SSE transport tests
- Implement stdio transport tests
- Implement basic MCP protocol tests

### Phase 3: Permission and Security Tests
- Implement permission profile tests
- Implement security constraint tests
- Implement edge case tests

### Phase 4: CI Integration and Reporting
- Set up CI workflow for E2E tests
- Implement test reporting
- Document test procedures