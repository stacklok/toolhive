use std::collections::HashMap;
use std::sync::{mpsc, Arc, Mutex};
use std::time::Duration;

use async_trait::async_trait;
use hyper::service::{make_service_fn, service_fn};
use hyper::{Body, Request, Response, Server, StatusCode};
use serde_json::{json, Value};
use tokio::io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt};
use tokio::sync::oneshot;

use vibetool::container::{ContainerInfo, ContainerRuntime};
use vibetool::error::{Error, Result};
use vibetool::permissions::profile::ContainerPermissionConfig;
use vibetool::transport::jsonrpc::JsonRpcMessage;
use vibetool::transport::sse::SseTransport;
use vibetool::transport::sse_common::{HTTP_MESSAGES, HTTP_SSE_ENDPOINT};
use vibetool::transport::stdio::StdioTransport;
use vibetool::transport::Transport;

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
        responses.insert(
            "initialize".to_string(),
            json!({
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
            }),
        );

        responses.insert(
            "resources/list".to_string(),
            json!({
                "resources": [
                    {
                        "name": "Test Resource",
                        "uri": "test://resource",
                        "description": "A test resource"
                    }
                ]
            }),
        );

        responses.insert(
            "tools/list".to_string(),
            json!({
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
            }),
        );

        responses.insert(
            "tools/call".to_string(),
            json!({
                "content": [
                    {
                        "type": "text",
                        "text": "Tool executed successfully"
                    }
                ]
            }),
        );

        responses.insert(
            "resources/read".to_string(),
            json!({
                "contents": [
                    {
                        "uri": "test://resource",
                        "text": "This is a test resource content"
                    }
                ]
            }),
        );

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
        let result = match &message.method {
            Some(method) => {
                if let Some(response) = self.responses.get(method) {
                    response.clone()
                } else {
                    json!({})
                }
            }
            None => json!({}),
        };

        JsonRpcMessage {
            jsonrpc: "2.0".to_string(),
            method: Some("response".to_string()),
            params: Some(result),
            id: message.id.clone(),
            result: None,
            error: None,
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
                        method: Some(event_type),
                        params: Some(params),
                        id: id.map(|id| json!(id)),
                        result: None,
                        error: None,
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
                    let msg = "message".to_string();
                    let sse_response = format!(
                        "event: {}\ndata: {}\n{}\n",
                        response.method.as_ref().unwrap_or(&msg),
                        sse_data,
                        sse_id.map(|id| format!("id: {}", id)).unwrap_or_default()
                    );

                    // Return the response
                    Ok::<_, hyper::Error>(
                        Response::builder()
                            .status(StatusCode::OK)
                            .header("Content-Type", "text/event-stream")
                            .body(Body::from(sse_response))
                            .unwrap(),
                    )
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
                                    match serde_json::from_str::<JsonRpcMessage>(
                                        &line_buffer.trim(),
                                    ) {
                                        Ok(json_rpc) => {
                                            // Process the message
                                            let response =
                                                self.fake_server.process_message(json_rpc);

                                            // Serialize to JSON
                                            let json_str = match serde_json::to_string(&response) {
                                                Ok(s) => s,
                                                Err(e) => {
                                                    eprintln!(
                                                        "Failed to serialize JSON-RPC: {}",
                                                        e
                                                    );
                                                    continue;
                                                }
                                            };

                                            // Add newline to ensure proper message separation
                                            let json_str = format!("{}\n", json_str);

                                            // Write to stdout
                                            if let Err(e) =
                                                stdout.write_all(json_str.as_bytes()).await
                                            {
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
    transport.set_container_port(9100);
    transport
        .setup(
            "test-id",
            "test-container",
            &mut env_vars,
            Some("127.0.0.1".to_string()),
        )
        .await?;

    // Start the transport
    transport.start(None, None).await?;

    // Create a client to send requests to the transport
    let client = reqwest::Client::new();

    // Send an initialize request
    let initialize_request = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: Some("initialize".to_string()),
        params: Some(
            json!({"clientInfo":{"name":"test-client","version":"0.1.0"},"capabilities":{},"protocolVersion":"0.1.0"}),
        ),
        id: Some(json!("1")),
        result: None,
        error: None,
    };

    // Format as SSE for the SSE transport
    let msg = "message".to_string();
    let event_type = initialize_request.method.as_ref().unwrap_or(&msg);
    let data = serde_json::to_string(&initialize_request.params).unwrap_or_default();
    let id = "1"; // Use a simple string for the ID

    let response = client
        .post("http://localhost:8100")
        .header("Content-Type", "text/event-stream")
        .body(format!(
            "event: {}\ndata: {}\nid: {}\n\n",
            event_type, data, id
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
    let list_request = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: Some("resources/list".to_string()),
        params: Some(json!({})),
        id: Some(json!("2")),
        result: None,
        error: None,
    };

    // Format as SSE for the SSE transport
    let msg = "message".to_string();
    let event_type = list_request.method.as_ref().unwrap_or(&msg);
    let data = serde_json::to_string(&list_request.params).unwrap_or_default();
    let id = "2"; // Use a simple string for the ID

    let response = client
        .post("http://localhost:8100")
        .header("Content-Type", "text/event-stream")
        .body(format!(
            "event: {}\ndata: {}\nid: {}\n\n",
            event_type, data, id
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
    assert_eq!(requests.len(), 4); // Now we expect 4 requests: 2 from our initialization + 2 from the test

    // The first two requests should be our initialization messages
    assert_eq!(requests[0].method, Some("initialize".to_string())); // Our initialization request
    assert_eq!(
        requests[1].method,
        Some("notifications/initialized".to_string())
    ); // Our initialized notification

    // The next two requests should be the test messages
    assert_eq!(requests[2].method, Some("initialize".to_string())); // Test's initialize request
    assert_eq!(requests[3].method, Some("resources/list".to_string())); // Test's resources/list request

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
    let transport = StdioTransport::new(8080);
    let mut env_vars = HashMap::new();

    // Set up the transport
    transport
        .setup("test-id", "test-container", &mut env_vars, None)
        .await?;

    // Start the transport (this would normally attach to the container)
    // For testing, we'll manually handle the IO

    // Send an initialize request
    let initialize_request = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: Some("initialize".to_string()),
        params: Some(
            json!({"clientInfo":{"name":"test-client","version":"0.1.0"},"capabilities":{},"protocolVersion":"0.1.0"}),
        ),
        id: Some(json!("1")),
        result: None,
        error: None,
    };

    let request_json =
        serde_json::to_string(&initialize_request).expect("Failed to serialize request");
    client_write
        .write_all(format!("{}\n", request_json).as_bytes())
        .await
        .expect("Failed to write request");

    // Read the response
    let mut buffer = Vec::new();
    let mut buf = [0u8; 1024];
    let n = client_read
        .read(&mut buf)
        .await
        .expect("Failed to read response");
    buffer.extend_from_slice(&buf[..n]);

    // Parse the response
    let response_str = std::str::from_utf8(&buffer).expect("Invalid UTF-8");
    let response: JsonRpcMessage =
        serde_json::from_str(response_str.trim()).expect("Failed to parse response");

    // Verify the response
    assert_eq!(response.method, Some("response".to_string()));
    assert!(response
        .params
        .as_ref()
        .unwrap()
        .get("capabilities")
        .is_some());
    assert!(response
        .params
        .as_ref()
        .unwrap()
        .get("protocolVersion")
        .is_some());
    assert!(response
        .params
        .as_ref()
        .unwrap()
        .get("serverInfo")
        .is_some());

    // Send a resources/list request
    let list_request = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: Some("resources/list".to_string()),
        params: Some(json!({})),
        id: Some(json!("2")),
        result: None,
        error: None,
    };

    let request_json = serde_json::to_string(&list_request).expect("Failed to serialize request");
    client_write
        .write_all(format!("{}\n", request_json).as_bytes())
        .await
        .expect("Failed to write request");

    // Read the response
    let mut buffer = Vec::new();
    let mut buf = [0u8; 1024];
    let n = client_read
        .read(&mut buf)
        .await
        .expect("Failed to read response");
    buffer.extend_from_slice(&buf[..n]);

    // Parse the response
    let response_str = std::str::from_utf8(&buffer).expect("Invalid UTF-8");
    let response: JsonRpcMessage =
        serde_json::from_str(response_str.trim()).expect("Failed to parse response");

    // Verify the response
    assert_eq!(response.method, Some("response".to_string()));
    assert!(response.params.as_ref().unwrap().get("resources").is_some());

    // Verify the fake server received the expected requests
    let requests = fake_server.get_requests();
    assert_eq!(requests.len(), 2); // This test doesn't use our transport.start() method, so we don't have the automatic initialization
    assert_eq!(requests[0].method, Some("initialize".to_string()));
    assert_eq!(requests[1].method, Some("resources/list".to_string()));

    Ok(())
}

// Mock ContainerRuntime implementation for testing the HTTP-to-stdio proxy
#[derive(Clone)]
struct MockContainerRuntimeForProxy {
    fake_server: Arc<FakeMcpServer>,
    container_id: String,
    container_name: String,
    // Channels to track messages sent to stdin and received from stdout
    stdin_messages: Arc<Mutex<Vec<String>>>,
    stdout_messages: Arc<Mutex<Vec<String>>>,
}

#[async_trait]
impl ContainerRuntime for MockContainerRuntimeForProxy {
    async fn create_container(
        &self,
        _image: &str,
        _name: &str,
        _command: Vec<String>,
        _env_vars: HashMap<String, String>,
        _labels: HashMap<String, String>,
        _permission_config: ContainerPermissionConfig,
    ) -> Result<String> {
        Ok(self.container_id.clone())
    }

    async fn start_container(&self, _container_id: &str) -> Result<()> {
        Ok(())
    }

    async fn list_containers(&self) -> Result<Vec<ContainerInfo>> {
        Ok(vec![])
    }

    async fn stop_container(&self, _container_id: &str) -> Result<()> {
        Ok(())
    }

    async fn remove_container(&self, _container_id: &str) -> Result<()> {
        Ok(())
    }

    async fn container_logs(&self, _container_id: &str) -> Result<String> {
        Ok("".to_string())
    }

    async fn is_container_running(&self, _container_id: &str) -> Result<bool> {
        Ok(true)
    }

    async fn get_container_info(&self, _container_id: &str) -> Result<ContainerInfo> {
        Err(Error::Transport("Not implemented".to_string()))
    }

    async fn get_container_ip(&self, _container_id: &str) -> Result<String> {
        Ok("127.0.0.1".to_string())
    }

    async fn attach_container(
        &self,
        _container_id: &str,
    ) -> Result<(
        Box<dyn AsyncWrite + Unpin + Send>,
        Box<dyn AsyncRead + Unpin + Send>,
    )> {
        // Create a mock stdin/stdout pair
        let stdin_messages = self.stdin_messages.clone();
        let stdout_messages = self.stdout_messages.clone();
        let fake_server = self.fake_server.clone();

        // Create a pipe for stdin
        let (stdin_tx, mut stdin_rx) = tokio::io::duplex(1024);
        // Create a pipe for stdout
        let (mut stdout_tx, stdout_rx) = tokio::io::duplex(1024);

        // Spawn a task to handle the container's stdin/stdout
        tokio::spawn(async move {
            let mut buffer = Vec::new();
            let mut line_buffer = String::new();

            // Process stdin
            loop {
                let mut buf = [0u8; 1024];
                match stdin_rx.read(&mut buf).await {
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

                                    // Store the message
                                    {
                                        let mut messages = stdin_messages.lock().unwrap();
                                        messages.push(line_buffer.clone());
                                    }

                                    // Try to parse as JSON-RPC
                                    if !line_buffer.trim().is_empty() {
                                        match serde_json::from_str::<JsonRpcMessage>(
                                            &line_buffer.trim(),
                                        ) {
                                            Ok(json_rpc) => {
                                                // Process the message
                                                let response =
                                                    fake_server.process_message(json_rpc);

                                                // Serialize to JSON
                                                if let Ok(json_str) =
                                                    serde_json::to_string(&response)
                                                {
                                                    // Add newline to ensure proper message separation
                                                    let json_str = format!("{}\n", json_str);

                                                    // Store the response
                                                    {
                                                        let mut messages =
                                                            stdout_messages.lock().unwrap();
                                                        messages.push(json_str.clone());
                                                    }

                                                    // Write to stdout
                                                    if let Err(e) = stdout_tx
                                                        .write_all(json_str.as_bytes())
                                                        .await
                                                    {
                                                        eprintln!(
                                                            "Failed to write to stdout: {}",
                                                            e
                                                        );
                                                        break;
                                                    }

                                                    // Flush stdout
                                                    if let Err(e) = stdout_tx.flush().await {
                                                        eprintln!("Failed to flush stdout: {}", e);
                                                        break;
                                                    }
                                                }
                                            }
                                            Err(e) => {
                                                eprintln!(
                                                    "Failed to parse JSON-RPC message: {}",
                                                    e
                                                );
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
        });

        Ok((Box::new(stdin_tx), Box::new(stdout_rx)))
    }
}

// Test the HTTP-to-stdio proxy functionality
#[tokio::test]
async fn test_stdio_transport_http_proxy() -> Result<()> {
    // Create a fake MCP server
    let fake_server = Arc::new(FakeMcpServer::new());

    // Create a mock container runtime
    let mock_runtime = MockContainerRuntimeForProxy {
        fake_server: fake_server.clone(),
        container_id: "test-container-id".to_string(),
        container_name: "test-container".to_string(),
        stdin_messages: Arc::new(Mutex::new(Vec::new())),
        stdout_messages: Arc::new(Mutex::new(Vec::new())),
    };

    // Create a STDIO transport with HTTP proxy on port 8300 (different from previous tests)
    let transport = StdioTransport::new(8300).with_runtime(Box::new(mock_runtime.clone()));

    // Set up the transport
    let mut env_vars = HashMap::new();
    transport
        .setup("test-container-id", "test-container", &mut env_vars, None)
        .await?;

    // Get stdin/stdout from the mock runtime
    let (stdin, stdout) = mock_runtime.attach_container("test-container-id").await?;

    // Start the transport with stdin/stdout
    transport.start(Some(stdin), Some(stdout)).await?;
    println!("DEBUG: Transport started");

    // Create a client to send requests to the transport
    let client = reqwest::Client::new();

    // We'll get the session ID from the SSE response

    // Create a new client for the SSE connection
    let sse_client = reqwest::Client::new();
    println!("DEBUG: Created SSE client");

    // Build the request
    let request = sse_client.get(format!("http://localhost:8300{}", HTTP_SSE_ENDPOINT));
    println!("DEBUG: Built SSE request: {:?}", request);

    // Send the request directly
    println!("DEBUG: Sending SSE request");
    let mut response = request.send().await.expect("Failed to send SSE request");
    println!("DEBUG: SSE response status: {:?}", response.status());

    // Read the first chunk
    println!("DEBUG: Reading first chunk");
    let chunk = response
        .chunk()
        .await
        .expect("Failed to read chunk")
        .expect("Empty chunk");
    let chunk_str = String::from_utf8_lossy(&chunk);
    println!("DEBUG: SSE chunk received: {}", chunk_str);

    // Extract the endpoint URL from the chunk
    let mut endpoint_url = String::new();
    for line in chunk_str.lines() {
        println!("DEBUG: SSE line: {}", line);
        if line.starts_with("data:") {
            endpoint_url = line[5..].trim().to_string();
            println!("DEBUG: Extracted endpoint URL: {}", endpoint_url);
            break;
        }
    }

    // Keep the SSE connection open in a separate task
    let mut response_clone = response;
    tokio::spawn(async move {
        println!("DEBUG: Keeping SSE connection open");
        loop {
            match response_clone.chunk().await {
                Ok(Some(chunk)) => {
                    let chunk_str = String::from_utf8_lossy(&chunk);
                    println!("DEBUG: Additional SSE chunk: {}", chunk_str);
                }
                Ok(None) => {
                    println!("DEBUG: SSE connection closed");
                    break;
                }
                Err(e) => {
                    println!("DEBUG: Error reading from SSE connection: {}", e);
                    break;
                }
            }
        }
    });

    if endpoint_url.is_empty() {
        panic!("Failed to extract endpoint URL from SSE response");
    }

    println!("Using endpoint URL: {}", endpoint_url);

    // Send an initialize request via HTTP JSON-RPC
    let initialize_request = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: Some("initialize".to_string()),
        params: Some(
            json!({"clientInfo":{"name":"test-client","version":"0.1.0"},"capabilities":{},"protocolVersion":"0.1.0"}),
        ),
        id: Some(json!("1")),
        result: None,
        error: None,
    };

    let request_json =
        serde_json::to_string(&initialize_request).expect("Failed to serialize request");
    let response = client
        .post(endpoint_url.clone())
        .header("Content-Type", "application/json")
        .body(request_json)
        .send()
        .await
        .expect("Failed to send request");

    // Our new implementation returns 202 Accepted instead of 200 OK
    println!("First request status: {:?}", response.status());
    assert_eq!(response.status(), StatusCode::ACCEPTED);

    // Give more time for the message to be processed
    tokio::time::sleep(tokio::time::Duration::from_millis(500)).await;

    // Verify that the message was sent to the container's stdin
    {
        let stdin_messages = mock_runtime.stdin_messages.lock().unwrap();
        assert!(!stdin_messages.is_empty());

        // The message should be a JSON-RPC message with the initialize method
        let message = &stdin_messages[0];
        let json_rpc: JsonRpcMessage =
            serde_json::from_str(message).expect("Failed to parse JSON-RPC message");
        assert_eq!(json_rpc.method, Some("initialize".to_string()));
        assert!(json_rpc
            .params
            .as_ref()
            .unwrap()
            .get("clientInfo")
            .is_some());
    }

    // Send a resources/list request
    let list_request = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: Some("resources/list".to_string()),
        params: Some(json!({})),
        id: Some(json!("2")),
        result: None,
        error: None,
    };

    let request_json = serde_json::to_string(&list_request).expect("Failed to serialize request");
    let response = client
        .post(endpoint_url.clone())
        .header("Content-Type", "application/json")
        .body(request_json)
        .send()
        .await
        .expect("Failed to send request");

    // Our implementation returns 202 Accepted for POST requests
    assert_eq!(response.status(), StatusCode::ACCEPTED);

    // Give more time for the message to be processed
    tokio::time::sleep(tokio::time::Duration::from_millis(500)).await;

    // Verify that the messages were sent to the container's stdin
    {
        let stdin_messages = mock_runtime.stdin_messages.lock().unwrap();
        assert!(stdin_messages.len() >= 2); // We expect at least the 2 messages we sent

        // Find the initialize message
        let init_message = stdin_messages
            .iter()
            .find(|msg| {
                if let Ok(json_rpc) = serde_json::from_str::<JsonRpcMessage>(msg) {
                    json_rpc.method == Some("initialize".to_string())
                } else {
                    false
                }
            })
            .expect("Initialize message not found");

        let init_json_rpc: JsonRpcMessage =
            serde_json::from_str(init_message).expect("Failed to parse JSON-RPC message");
        assert_eq!(init_json_rpc.method, Some("initialize".to_string()));
        assert!(init_json_rpc
            .params
            .as_ref()
            .unwrap()
            .get("clientInfo")
            .is_some());

        // Find the resources/list message
        let list_message = stdin_messages
            .iter()
            .find(|msg| {
                if let Ok(json_rpc) = serde_json::from_str::<JsonRpcMessage>(msg) {
                    json_rpc.method == Some("resources/list".to_string())
                } else {
                    false
                }
            })
            .expect("resources/list message not found");

        let list_json_rpc: JsonRpcMessage =
            serde_json::from_str(list_message).expect("Failed to parse JSON-RPC message");
        assert_eq!(list_json_rpc.method, Some("resources/list".to_string()));
    }

    // Verify the fake server received the expected requests
    let requests = fake_server.get_requests();
    assert!(requests.len() >= 2); // We expect at least the 2 requests we sent

    // Find the initialize request
    let init_request = requests
        .iter()
        .find(|req| req.method == Some("initialize".to_string()))
        .expect("Initialize request not found");
    assert_eq!(init_request.method, Some("initialize".to_string()));

    // Find the resources/list request
    let list_request = requests
        .iter()
        .find(|req| req.method == Some("resources/list".to_string()))
        .expect("resources/list request not found");
    assert_eq!(list_request.method, Some("resources/list".to_string()));

    // Stop the transport
    transport.stop().await?;

    Ok(())
}

// Test JSON-RPC message creation
#[test]
fn test_format_conversion() {
    // Create a JSON-RPC message
    let json_rpc = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: Some("initialize".to_string()),
        params: Some(
            json!({"clientInfo":{"name":"test-client","version":"0.1.0"},"capabilities":{},"protocolVersion":"0.1.0"}),
        ),
        id: Some(json!("1")),
        result: None,
        error: None,
    };

    // Verify the message
    assert_eq!(json_rpc.jsonrpc, "2.0");
    assert_eq!(json_rpc.method, Some("initialize".to_string()));
    assert_eq!(
        json_rpc.params.as_ref().unwrap()["clientInfo"]["name"],
        "test-client"
    );
    assert_eq!(json_rpc.id.as_ref().unwrap().as_str().unwrap(), "1");
    assert!(json_rpc.result.is_none());
    assert!(json_rpc.error.is_none());

    // Serialize and deserialize
    let json_str = serde_json::to_string(&json_rpc).expect("Failed to serialize");
    let deserialized: JsonRpcMessage =
        serde_json::from_str(&json_str).expect("Failed to deserialize");

    // Verify the deserialized message
    assert_eq!(deserialized.jsonrpc, "2.0");
    assert_eq!(deserialized.method, Some("initialize".to_string()));
    assert_eq!(
        deserialized.params.as_ref().unwrap()["clientInfo"]["name"],
        "test-client"
    );
    assert_eq!(deserialized.id.as_ref().unwrap().as_str().unwrap(), "1");
    assert!(deserialized.result.is_none());
    assert!(deserialized.error.is_none());
}
