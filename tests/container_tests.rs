use std::io::{Read, Write};
use std::net::TcpStream;
use std::thread;
use std::time::Duration;

// Import the necessary modules
use mcp_lok::container::ContainerManager;
use mcp_lok::permissions::PermissionProfile;

// Mock MCP server that responds to JSON-RPC requests
struct MockMcpServer {
    port: u16,
}

impl MockMcpServer {
    fn new(port: u16) -> Self {
        MockMcpServer { port }
    }

    fn start(&self) {
        let port = self.port;
        thread::spawn(move || {
            // Create a simple HTTP server that responds to JSON-RPC requests
            let listener = std::net::TcpListener::bind(format!("127.0.0.1:{}", port)).unwrap();
            println!("Mock MCP server listening on port {}", port);

            for stream in listener.incoming() {
                match stream {
                    Ok(mut stream) => {
                        // Handle the connection
                        let mut buffer = [0; 1024];
                        let n = stream.read(&mut buffer).unwrap();
                        let request = String::from_utf8_lossy(&buffer[..n]);
                        
                        println!("Mock server received: {}", request);
                        
                        // Check if this is a GET request for SSE
                        if request.starts_with("GET") {
                            // Send SSE response
                            let response = "HTTP/1.1 200 OK\r\n\
                                           Content-Type: text/event-stream\r\n\
                                           Cache-Control: no-cache\r\n\
                                           Connection: keep-alive\r\n\
                                           \r\n\
                                           event: message\r\ndata: {\"type\":\"test\",\"message\":\"Hello from mock MCP server!\"}\r\n\r\n";
                            
                            stream.write_all(response.as_bytes()).unwrap();
                            stream.flush().unwrap();
                            
                            // Keep the connection open for a while
                            thread::sleep(Duration::from_secs(1));
                        } else if request.starts_with("POST") {
                            // Extract the JSON-RPC request
                            if let Some(body_start) = request.find("\r\n\r\n") {
                                let body = &request[body_start + 4..];
                                println!("JSON-RPC request: {}", body);
                                
                                // Check the method and respond accordingly
                                if body.contains("\"method\":\"initialize\"") {
                                    // Send initialize response
                                    let response = "HTTP/1.1 200 OK\r\n\
                                                   Content-Type: application/json\r\n\
                                                   Content-Length: 203\r\n\
                                                   \r\n\
                                                   {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"serverInfo\":{\"name\":\"mock-mcp-server\",\"version\":\"1.0.0\"},\"protocolVersion\":\"0.1.0\",\"capabilities\":{\"resources\":{},\"tools\":{}}}}";
                                    
                                    stream.write_all(response.as_bytes()).unwrap();
                                    stream.flush().unwrap();
                                } else if body.contains("\"method\":\"resources/list\"") {
                                    // Send resources list response
                                    let response = "HTTP/1.1 200 OK\r\n\
                                                   Content-Type: application/json\r\n\
                                                   Content-Length: 146\r\n\
                                                   \r\n\
                                                   {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"resources\":[{\"uri\":\"mock://info\",\"name\":\"Mock Info\",\"description\":\"Information about the mock server\"}]}}";
                                    
                                    stream.write_all(response.as_bytes()).unwrap();
                                    stream.flush().unwrap();
                                } else if body.contains("\"method\":\"tools/list\"") {
                                    // Send tools list response
                                    let response = "HTTP/1.1 200 OK\r\n\
                                                   Content-Type: application/json\r\n\
                                                   Content-Length: 183\r\n\
                                                   \r\n\
                                                   {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"name\":\"echo\",\"description\":\"Echo the input text\",\"inputSchema\":{\"type\":\"object\",\"properties\":{\"text\":{\"type\":\"string\"}}}}]}}";
                                    
                                    stream.write_all(response.as_bytes()).unwrap();
                                    stream.flush().unwrap();
                                } else if body.contains("\"method\":\"tools/call\"") {
                                    // Send echo tool response
                                    let response = "HTTP/1.1 200 OK\r\n\
                                                   Content-Type: application/json\r\n\
                                                   Content-Length: 109\r\n\
                                                   \r\n\
                                                   {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"Echo: Hello from test!\"}]}}";
                                    
                                    stream.write_all(response.as_bytes()).unwrap();
                                    stream.flush().unwrap();
                                } else {
                                    // Send generic error response
                                    let response = "HTTP/1.1 200 OK\r\n\
                                                   Content-Type: application/json\r\n\
                                                   Content-Length: 97\r\n\
                                                   \r\n\
                                                   {\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-32601,\"message\":\"Method not found\"}}";
                                    
                                    stream.write_all(response.as_bytes()).unwrap();
                                    stream.flush().unwrap();
                                }
                            }
                        }
                    }
                    Err(e) => {
                        println!("Error accepting connection: {}", e);
                    }
                }
            }
        });
        
        // Give the server a moment to start
        thread::sleep(Duration::from_millis(100));
    }
}

// Helper function to send a JSON-RPC request and get the response
fn send_jsonrpc_request(port: u16, method: &str, params: Option<&str>) -> String {
    // Create a JSON-RPC request
    let params_str = params.unwrap_or("{}");
    let request = format!(
        "{{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"{}\",\"params\":{}}}",
        method, params_str
    );
    
    // Send the request via HTTP POST
    let mut stream = TcpStream::connect(format!("127.0.0.1:{}", port)).unwrap();
    let http_request = format!(
        "POST / HTTP/1.1\r\n\
         Host: 127.0.0.1:{}\r\n\
         Content-Type: application/json\r\n\
         Content-Length: {}\r\n\
         \r\n\
         {}",
        port, request.len(), request
    );
    
    stream.write_all(http_request.as_bytes()).unwrap();
    stream.flush().unwrap();
    
    // Read the response
    let mut response = String::new();
    stream.read_to_string(&mut response).unwrap();
    
    response
}

#[tokio::test]
async fn test_proxy_get_request() {
    // Start a mock MCP server
    let mock_server = MockMcpServer::new(8081);
    mock_server.start();
    
    // Create a container manager
    let container_manager = ContainerManager::new().await.unwrap();
    
    // Create a minimal permission profile
    let profile = PermissionProfile {
        read: vec![],
        write: vec![],
        network: None,
    };
    
    // Run a container with the mock server
    let container_id = container_manager.run_container(
        "test-proxy",
        "alpine:latest",
        mcp_lok::container::TransportMode::STDIO,
        Some(8082),
        &profile,
        &["sleep".to_string(), "3600".to_string()],
        false,
    ).await.unwrap();
    
    // Give the proxy a moment to start
    thread::sleep(Duration::from_millis(500));
    
    // Send a GET request to the proxy
    let mut stream = TcpStream::connect("127.0.0.1:8082").unwrap();
    let request = "GET / HTTP/1.1\r\nHost: 127.0.0.1:8082\r\n\r\n";
    stream.write_all(request.as_bytes()).unwrap();
    stream.flush().unwrap();
    
    // Read the response
    let mut buffer = [0; 1024];
    let n = stream.read(&mut buffer).unwrap();
    let response = String::from_utf8_lossy(&buffer[..n]);
    
    // Verify the response
    assert!(response.contains("HTTP/1.1 200 OK"));
    assert!(response.contains("Content-Type: text/event-stream"));
    
    // Clean up
    container_manager.remove_container("test-proxy").await.unwrap();
}

#[tokio::test]
async fn test_proxy_initialize_request() {
    // Start a mock MCP server
    let mock_server = MockMcpServer::new(8083);
    mock_server.start();
    
    // Create a container manager
    let container_manager = ContainerManager::new().await.unwrap();
    
    // Create a minimal permission profile
    let profile = PermissionProfile {
        read: vec![],
        write: vec![],
        network: None,
    };
    
    // Run a container with the mock server
    let container_id = container_manager.run_container(
        "test-proxy-init",
        "alpine:latest",
        mcp_lok::container::TransportMode::STDIO,
        Some(8084),
        &profile,
        &["sleep".to_string(), "3600".to_string()],
        false,
    ).await.unwrap();
    
    // Give the proxy a moment to start
    thread::sleep(Duration::from_millis(500));
    
    // Send an initialize request
    let response = send_jsonrpc_request(
        8084,
        "initialize",
        Some("{\"clientInfo\":{\"name\":\"test-client\",\"version\":\"1.0.0\"},\"protocolVersion\":\"0.1.0\",\"capabilities\":{}}")
    );
    
    // Verify the response
    assert!(response.contains("HTTP/1.1 200 OK"));
    assert!(response.contains("Content-Type: application/json"));
    assert!(response.contains("\"serverInfo\":{\"name\":\"mock-mcp-server\",\"version\":\"1.0.0\"}"));
    
    // Clean up
    container_manager.remove_container("test-proxy-init").await.unwrap();
}

#[tokio::test]
async fn test_proxy_resources_list_request() {
    // Start a mock MCP server
    let mock_server = MockMcpServer::new(8085);
    mock_server.start();
    
    // Create a container manager
    let container_manager = ContainerManager::new().await.unwrap();
    
    // Create a minimal permission profile
    let profile = PermissionProfile {
        read: vec![],
        write: vec![],
        network: None,
    };
    
    // Run a container with the mock server
    let container_id = container_manager.run_container(
        "test-proxy-resources",
        "alpine:latest",
        mcp_lok::container::TransportMode::STDIO,
        Some(8086),
        &profile,
        &["sleep".to_string(), "3600".to_string()],
        false,
    ).await.unwrap();
    
    // Give the proxy a moment to start
    thread::sleep(Duration::from_millis(500));
    
    // Send a resources/list request
    let response = send_jsonrpc_request(8086, "resources/list", None);
    
    // Verify the response
    assert!(response.contains("HTTP/1.1 200 OK"));
    assert!(response.contains("Content-Type: application/json"));
    assert!(response.contains("\"resources\":[{\"uri\":\"mock://info\",\"name\":\"Mock Info\",\"description\":\"Information about the mock server\"}]"));
    
    // Clean up
    container_manager.remove_container("test-proxy-resources").await.unwrap();
}

#[tokio::test]
async fn test_proxy_tools_list_request() {
    // Start a mock MCP server
    let mock_server = MockMcpServer::new(8087);
    mock_server.start();
    
    // Create a container manager
    let container_manager = ContainerManager::new().await.unwrap();
    
    // Create a minimal permission profile
    let profile = PermissionProfile {
        read: vec![],
        write: vec![],
        network: None,
    };
    
    // Run a container with the mock server
    let container_id = container_manager.run_container(
        "test-proxy-tools",
        "alpine:latest",
        mcp_lok::container::TransportMode::STDIO,
        Some(8088),
        &profile,
        &["sleep".to_string(), "3600".to_string()],
        false,
    ).await.unwrap();
    
    // Give the proxy a moment to start
    thread::sleep(Duration::from_millis(500));
    
    // Send a tools/list request
    let response = send_jsonrpc_request(8088, "tools/list", None);
    
    // Verify the response
    assert!(response.contains("HTTP/1.1 200 OK"));
    assert!(response.contains("Content-Type: application/json"));
    assert!(response.contains("\"tools\":[{\"name\":\"echo\",\"description\":\"Echo the input text\""));
    
    // Clean up
    container_manager.remove_container("test-proxy-tools").await.unwrap();
}

#[tokio::test]
async fn test_proxy_tool_call_request() {
    // Start a mock MCP server
    let mock_server = MockMcpServer::new(8089);
    mock_server.start();
    
    // Create a container manager
    let container_manager = ContainerManager::new().await.unwrap();
    
    // Create a minimal permission profile
    let profile = PermissionProfile {
        read: vec![],
        write: vec![],
        network: None,
    };
    
    // Run a container with the mock server
    let container_id = container_manager.run_container(
        "test-proxy-call",
        "alpine:latest",
        mcp_lok::container::TransportMode::STDIO,
        Some(8090),
        &profile,
        &["sleep".to_string(), "3600".to_string()],
        false,
    ).await.unwrap();
    
    // Give the proxy a moment to start
    thread::sleep(Duration::from_millis(500));
    
    // Send a tools/call request
    let response = send_jsonrpc_request(
        8090,
        "tools/call",
        Some("{\"name\":\"echo\",\"arguments\":{\"text\":\"Hello from test!\"}}")
    );
    
    // Verify the response
    assert!(response.contains("HTTP/1.1 200 OK"));
    assert!(response.contains("Content-Type: application/json"));
    assert!(response.contains("\"content\":[{\"type\":\"text\",\"text\":\"Echo: Hello from test!\"}]"));
    
    // Clean up
    container_manager.remove_container("test-proxy-call").await.unwrap();
}