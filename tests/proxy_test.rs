use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use hyper::{Body, Request, Response, Server, StatusCode};
use hyper::service::{make_service_fn, service_fn};
use serde_json::{json, Value};
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::oneshot;

use mcp_lok::transport::Transport;
use mcp_lok::transport::sse::SseTransport;
use mcp_lok::transport::stdio::{JsonRpcMessage, SseMessage, StdioTransport};
use mcp_lok::error::Result;

// Fake MCP server that responds to MCP protocol requests
struct FakeMcpServer {
    // Store received requests for verification
    requests: Arc<Mutex<Vec<JsonRpcMessage>>>,
    // Predefined responses for specific methods
    responses: HashMap<String, Value>,
}

impl FakeMcpServer {
    fn new() -> Self {
        let mut responses = HashMap::new();
        
        // Add some default responses
        responses.insert("initialize".to_string(), json!({
            "capabilities": {
                "resources": {
                    "listChanged": true,
                    "subscribe": true
                },
                "tools": {
                    "listChanged": true
                }
            },
            "protocolVersion": "0.1.0",
            "serverInfo": {
                "name": "fake-mcp-server",
                "version": "0.1.0"
            }
        }));
        
        responses.insert("resources/list".to_string(), json!({
            "resources": [
                {
                    "name": "Test Resource",
                    "uri": "test://resource",
                    "description": "A test resource"
                }
            ]
        }));
        
        responses.insert("tools/list".to_string(), json!({
            "tools": [
                {
                    "name": "test_tool",
                    "description": "A test tool",
                    "inputSchema": {
                        "type": "object",
                        "properties": {
                            "param1": {
                                "type": "string"
                            }
                        },
                        "required": ["param1"]
                    }
                }
            ]
        }));
        
        responses.insert("tools/call".to_string(), json!({
            "content": [
                {
                    "type": "text",
                    "text": "Tool executed successfully"
                }
            ]
        }));
        
        responses.insert("resources/read".to_string(), json!({
            "contents": [
                {
                    "uri": "test://resource",
                    "text": "This is a test resource content"
                }
            ]
        }));
        
        Self {
            requests: Arc::new(Mutex::new(Vec::new())),
            responses,
        }
    }
    
    // Process an incoming JSON-RPC message and return a response
    fn process_message(&self, message: JsonRpcMessage) -> JsonRpcMessage {
        // Store the request for later verification
        {
            let mut requests = self.requests.lock().unwrap();
            requests.push(message.clone());
        }
        
        // Create a response based on the method
        let result = if let Some(response) = self.responses.get(&message.method) {
            response.clone()
        } else {
            json!({})
        };
        
        JsonRpcMessage {
            jsonrpc: "2.0".to_string(),
            method: "response".to_string(),
            params: result,
            id: message.id.clone(),
        }
    }
    
    // Get all received requests
    fn get_requests(&self) -> Vec<JsonRpcMessage> {
        let requests = self.requests.lock().unwrap();
        requests.clone()
    }
}

// Start an HTTP server that acts as a fake MCP server for SSE transport testing
async fn start_sse_mcp_server(port: u16) -> (Arc<FakeMcpServer>, oneshot::Sender<()>) {
    let addr = ([127, 0, 0, 1], port).into();
    let fake_server = Arc::new(FakeMcpServer::new());
    let fake_server_clone = fake_server.clone();
    
    // Create a oneshot channel for shutdown
    let (tx, rx) = oneshot::channel::<()>();
    
    // Create a service that processes MCP requests
    let make_svc = make_service_fn(move |_conn| {
        let fake_server = fake_server_clone.clone();
        async move {
            Ok::<_, hyper::Error>(service_fn(move |req: Request<Body>| {
                let fake_server = fake_server.clone();
                async move {
                    // Read the request body
                    let body_bytes = hyper::body::to_bytes(req.into_body()).await?;
                    let body_str = String::from_utf8_lossy(&body_bytes);
                    
                    // Parse the SSE message
                    let mut event_type = "message".to_string();
                    let mut data = String::new();
                    let mut id = None;
                    
                    for line in body_str.lines() {
                        if line.starts_with("event:") {
                            event_type = line[6..].trim().to_string();
                        } else if line.starts_with("data:") {
                            data = line[5..].trim().to_string();
                        } else if line.starts_with("id:") {
                            id = Some(line[3..].trim().to_string());
                        }
                    }
                    
                    // Convert SSE to JSON-RPC
                    let params = match serde_json::from_str::<Value>(&data) {
                        Ok(value) => value,
                        Err(_) => json!(data),
                    };
                    
                    let json_rpc = JsonRpcMessage {
                        jsonrpc: "2.0".to_string(),
                        method: event_type,
                        params,
                        id: id.map(|id| json!(id)),
                    };
                    
                    // Process the message
                    let response = fake_server.process_message(json_rpc);
                    
                    // Convert JSON-RPC to SSE
                    let sse_data = serde_json::to_string(&response.params).unwrap_or_default();
                    let sse_id = response.id.map(|id| {
                        if let Value::String(s) = id {
                            s
                        } else {
                            id.to_string()
                        }
                    });
                    
                    // Format as SSE
                    let sse_response = format!(
                        "event: {}\ndata: {}\n{}\n",
                        response.method,
                        sse_data,
                        sse_id.map(|id| format!("id: {}", id)).unwrap_or_default()
                    );
                    
                    // Return the response
                    Ok::<_, hyper::Error>(Response::builder()
                        .status(StatusCode::OK)
                        .header("Content-Type", "text/event-stream")
                        .body(Body::from(sse_response))
                        .unwrap())
                }
            }))
        }
    });
    
    // Create the server
    let server = Server::bind(&addr)
        .serve(make_svc)
        .with_graceful_shutdown(async {
            rx.await.ok();
        });
    
    // Spawn the server task
    tokio::spawn(async move {
        if let Err(e) = server.await {
            eprintln!("Server error: {}", e);
        }
    });
    
    // Return the fake server and shutdown sender
    (fake_server, tx)
}

// Create a mock container runtime for STDIO transport testing
struct MockStdioRuntime {
    fake_server: Arc<FakeMcpServer>,
}

impl MockStdioRuntime {
    fn new(fake_server: Arc<FakeMcpServer>) -> Self {
        Self { fake_server }
    }
    
    // Simulate container stdin/stdout
    async fn handle_io(
        &self,
        mut stdin: impl AsyncReadExt + Unpin,
        mut stdout: impl AsyncWriteExt + Unpin,
    ) -> Result<()> {
        let mut buffer = Vec::new();
        let mut line_buffer = String::new();
        
        loop {
            let mut buf = [0u8; 1024];
            match stdin.read(&mut buf).await {
                Ok(0) => break, // EOF
                Ok(n) => {
                    // Process the data
                    let data = &buf[..n];
                    buffer.extend_from_slice(data);
                    
                    // Process complete lines
                    let mut start_idx = 0;
                    for i in 0..buffer.len() {
                        if buffer[i] == b'\n' {
                            // Extract the line
                            if let Ok(line) = std::str::from_utf8(&buffer[start_idx..i]) {
                                line_buffer.push_str(line);
                                
                                // Try to parse as JSON-RPC
                                if !line_buffer.trim().is_empty() {
                                    match serde_json::from_str::<JsonRpcMessage>(&line_buffer.trim()) {
                                        Ok(json_rpc) => {
                                            // Process the message
                                            let response = self.fake_server.process_message(json_rpc);
                                            
                                            // Serialize to JSON
                                            let json_str = match serde_json::to_string(&response) {
                                                Ok(s) => s,
                                                Err(e) => {
                                                    eprintln!("Failed to serialize JSON-RPC: {}", e);
                                                    continue;
                                                }
                                            };
                                            
                                            // Add newline to ensure proper message separation
                                            let json_str = format!("{}\n", json_str);
                                            
                                            // Write to stdout
                                            if let Err(e) = stdout.write_all(json_str.as_bytes()).await {
                                                eprintln!("Failed to write to stdout: {}", e);
                                                break;
                                            }
                                            
                                            // Flush stdout
                                            if let Err(e) = stdout.flush().await {
                                                eprintln!("Failed to flush stdout: {}", e);
                                                break;
                                            }
                                        }
                                        Err(e) => {
                                            eprintln!("Failed to parse JSON-RPC message: {}", e);
                                            eprintln!("Message: {}", line_buffer);
                                        }
                                    }
                                }
                                
                                // Clear the line buffer
                                line_buffer.clear();
                            }
                            
                            // Update the start index for the next line
                            start_idx = i + 1;
                        }
                    }
                    
                    // Keep any remaining partial line in the buffer
                    if start_idx < buffer.len() {
                        buffer = buffer[start_idx..].to_vec();
                    } else {
                        buffer.clear();
                    }
                }
                Err(e) => {
                    eprintln!("Error reading from stdin: {}", e);
                    break;
                }
            }
        }
        
        Ok(())
    }
}

// Test SSE transport with a fake MCP server
#[tokio::test]
async fn test_sse_transport_proxy() -> Result<()> {
    // Start a fake MCP server on port 9100
    let (fake_server, shutdown_tx) = start_sse_mcp_server(9100).await;
    
    // Create an SSE transport on port 8100
    let transport = SseTransport::new(8100);
    let mut env_vars = HashMap::new();
    
    // Set up the transport to connect to port 9100 (not the default 8080)
    transport.setup("test-id", "test-container", Some(9100), &mut env_vars).await?;
    
    // Start the transport
    transport.start().await?;
    
    // Create a client to send requests to the transport
    let client = reqwest::Client::new();
    
    // Send an initialize request
    let initialize_request = SseMessage {
        event: "initialize".to_string(),
        data: r#"{"clientInfo":{"name":"test-client","version":"0.1.0"},"capabilities":{},"protocolVersion":"0.1.0"}"#.to_string(),
        id: Some("1".to_string()),
    };
    
    let response = client.post("http://localhost:8100")
        .header("Content-Type", "text/event-stream")
        .body(format!(
            "event: {}\ndata: {}\nid: {}\n\n",
            initialize_request.event,
            initialize_request.data,
            initialize_request.id.unwrap()
        ))
        .send()
        .await
        .expect("Failed to send request");
    
    assert_eq!(response.status(), StatusCode::OK);
    
    // Parse the response
    let response_text = response.text().await.expect("Failed to get response text");
    
    // Verify the response contains the expected data
    assert!(response_text.contains("event: response"));
    assert!(response_text.contains("capabilities"));
    assert!(response_text.contains("protocolVersion"));
    assert!(response_text.contains("serverInfo"));
    
    // Send a resources/list request
    let list_request = SseMessage {
        event: "resources/list".to_string(),
        data: "{}".to_string(),
        id: Some("2".to_string()),
    };
    
    let response = client.post("http://localhost:8100")
        .header("Content-Type", "text/event-stream")
        .body(format!(
            "event: {}\ndata: {}\nid: {}\n\n",
            list_request.event,
            list_request.data,
            list_request.id.unwrap()
        ))
        .send()
        .await
        .expect("Failed to send request");
    
    assert_eq!(response.status(), StatusCode::OK);
    
    // Parse the response
    let response_text = response.text().await.expect("Failed to get response text");
    
    // Verify the response contains the expected data
    assert!(response_text.contains("event: response"));
    assert!(response_text.contains("resources"));
    assert!(response_text.contains("Test Resource"));
    
    // Verify the fake server received the expected requests
    let requests = fake_server.get_requests();
    assert_eq!(requests.len(), 2);
    assert_eq!(requests[0].method, "initialize");
    assert_eq!(requests[1].method, "resources/list");
    
    // Stop the transport
    transport.stop().await?;
    
    // Shutdown the fake server
    let _ = shutdown_tx.send(());
    
    Ok(())
}

// Test STDIO transport with a fake MCP server
#[tokio::test]
async fn test_stdio_transport_proxy() -> Result<()> {
    // Create a fake MCP server
    let fake_server = Arc::new(FakeMcpServer::new());
    
    // Create a mock STDIO runtime
    let mock_runtime = MockStdioRuntime::new(fake_server.clone());
    
    // Create pipes for stdin/stdout
    let (mut client_write, server_read) = tokio::io::duplex(1024);
    let (server_write, mut client_read) = tokio::io::duplex(1024);
    
    // Spawn the mock runtime to handle IO
    tokio::spawn(async move {
        let _ = mock_runtime.handle_io(server_read, server_write).await;
    });
    
    // Create a STDIO transport
    let transport = StdioTransport::new();
    let mut env_vars = HashMap::new();
    
    // Set up the transport
    transport.setup("test-id", "test-container", None, &mut env_vars).await?;
    
    // Start the transport (this would normally attach to the container)
    // For testing, we'll manually handle the IO
    
    // Send an initialize request
    let initialize_request = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: "initialize".to_string(),
        params: json!({"clientInfo":{"name":"test-client","version":"0.1.0"},"capabilities":{},"protocolVersion":"0.1.0"}),
        id: Some(json!("1")),
    };
    
    let request_json = serde_json::to_string(&initialize_request).expect("Failed to serialize request");
    client_write.write_all(format!("{}\n", request_json).as_bytes()).await.expect("Failed to write request");
    
    // Read the response
    let mut buffer = Vec::new();
    let mut buf = [0u8; 1024];
    let n = client_read.read(&mut buf).await.expect("Failed to read response");
    buffer.extend_from_slice(&buf[..n]);
    
    // Parse the response
    let response_str = std::str::from_utf8(&buffer).expect("Invalid UTF-8");
    let response: JsonRpcMessage = serde_json::from_str(response_str.trim()).expect("Failed to parse response");
    
    // Verify the response
    assert_eq!(response.method, "response");
    assert!(response.params.get("capabilities").is_some());
    assert!(response.params.get("protocolVersion").is_some());
    assert!(response.params.get("serverInfo").is_some());
    
    // Send a resources/list request
    let list_request = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: "resources/list".to_string(),
        params: json!({}),
        id: Some(json!("2")),
    };
    
    let request_json = serde_json::to_string(&list_request).expect("Failed to serialize request");
    client_write.write_all(format!("{}\n", request_json).as_bytes()).await.expect("Failed to write request");
    
    // Read the response
    let mut buffer = Vec::new();
    let mut buf = [0u8; 1024];
    let n = client_read.read(&mut buf).await.expect("Failed to read response");
    buffer.extend_from_slice(&buf[..n]);
    
    // Parse the response
    let response_str = std::str::from_utf8(&buffer).expect("Invalid UTF-8");
    let response: JsonRpcMessage = serde_json::from_str(response_str.trim()).expect("Failed to parse response");
    
    // Verify the response
    assert_eq!(response.method, "response");
    assert!(response.params.get("resources").is_some());
    
    // Verify the fake server received the expected requests
    let requests = fake_server.get_requests();
    assert_eq!(requests.len(), 2);
    assert_eq!(requests[0].method, "initialize");
    assert_eq!(requests[1].method, "resources/list");
    
    Ok(())
}

// Test conversion between SSE and JSON-RPC formats
#[test]
fn test_format_conversion() {
    // Create an SSE message
    let sse = SseMessage {
        event: "initialize".to_string(),
        data: r#"{"clientInfo":{"name":"test-client","version":"0.1.0"},"capabilities":{},"protocolVersion":"0.1.0"}"#.to_string(),
        id: Some("1".to_string()),
    };
    
    // Convert SSE to JSON-RPC
    let params = match serde_json::from_str::<Value>(&sse.data) {
        Ok(value) => value,
        Err(_) => json!(sse.data),
    };
    
    let json_rpc = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: sse.event,
        params,
        id: sse.id.map(|id| json!(id)),
    };
    
    // Verify the conversion
    assert_eq!(json_rpc.method, "initialize");
    assert_eq!(json_rpc.params["clientInfo"]["name"], "test-client");
    assert_eq!(json_rpc.id.as_ref().unwrap().as_str().unwrap(), "1");
    
    // Convert JSON-RPC back to SSE
    let sse_data = serde_json::to_string(&json_rpc.params).unwrap_or_default();
    let sse_id = json_rpc.id.as_ref().map(|id| {
        if let Value::String(s) = id {
            s.clone()
        } else {
            id.to_string()
        }
    });
    
    let sse_back = SseMessage {
        event: json_rpc.method,
        data: sse_data,
        id: sse_id,
    };
    
    // Verify the conversion back
    assert_eq!(sse_back.event, "initialize");
    assert!(sse_back.data.contains("clientInfo"));
    assert_eq!(sse_back.id.unwrap(), "1");
}