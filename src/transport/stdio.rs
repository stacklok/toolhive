use async_trait::async_trait;
use hyper::{Body, Request, Response, Server, StatusCode};
use hyper::service::{make_service_fn, service_fn};
use serde::{Deserialize, Serialize};
use std::any::Any;
use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::{mpsc, oneshot, Mutex};

use crate::container::ContainerRuntime;
use crate::error::{Error, Result};
use crate::transport::{Transport, TransportMode};

/// STDIO transport handler for MCP servers
#[derive(Clone)]
pub struct StdioTransport {
    port: Option<u16>,
    container_id: Arc<Mutex<Option<String>>>,
    container_name: Arc<Mutex<Option<String>>>,
    runtime: Arc<Mutex<Option<Box<dyn ContainerRuntime>>>>,
    shutdown_tx: Arc<Mutex<Option<oneshot::Sender<()>>>>,
    message_tx: Arc<Mutex<Option<mpsc::Sender<SseMessage>>>>,
    response_rx: Arc<Mutex<Option<mpsc::Receiver<JsonRpcMessage>>>>,
    http_shutdown_tx: Arc<Mutex<Option<oneshot::Sender<()>>>>,
}

impl StdioTransport {
    /// Create a new STDIO transport handler
    pub fn new() -> Self {
        Self {
            port: None,
            container_id: Arc::new(Mutex::new(None)),
            container_name: Arc::new(Mutex::new(None)),
            runtime: Arc::new(Mutex::new(None)),
            shutdown_tx: Arc::new(Mutex::new(None)),
            message_tx: Arc::new(Mutex::new(None)),
            response_rx: Arc::new(Mutex::new(None)),
            http_shutdown_tx: Arc::new(Mutex::new(None)),
        }
    }

    /// Create a new STDIO transport handler with a specific port
    pub fn with_port(port: u16) -> Self {
        Self {
            port: Some(port),
            container_id: Arc::new(Mutex::new(None)),
            container_name: Arc::new(Mutex::new(None)),
            runtime: Arc::new(Mutex::new(None)),
            shutdown_tx: Arc::new(Mutex::new(None)),
            message_tx: Arc::new(Mutex::new(None)),
            response_rx: Arc::new(Mutex::new(None)),
            http_shutdown_tx: Arc::new(Mutex::new(None)),
        }
    }

    /// Set the container runtime
    pub fn with_runtime(mut self, runtime: Box<dyn ContainerRuntime>) -> Self {
        // Create a new runtime with the provided runtime
        let new_runtime = Arc::new(Mutex::new(Some(runtime)));
        self.runtime = new_runtime;
        self
    }

    /// Start the HTTP server for the reverse proxy
    async fn start_http_server(&self, port: u16) -> Result<()> {
        // Get container ID and name
        let container_id = {
            let guard = self.container_id.lock().await;
            guard.clone()
        };
        
        let container_id = match container_id {
            Some(id) => id,
            None => return Err(Error::Transport("Container ID not set".to_string())),
        };

        let container_name = {
            let guard = self.container_name.lock().await;
            guard.clone()
        };
        
        let container_name = match container_name {
            Some(name) => name,
            None => return Err(Error::Transport("Container name not set".to_string())),
        };

        // Clone the message sender for use in the service
        let message_tx = {
            let guard = self.message_tx.lock().await;
            guard.clone()
        };
        
        let message_tx = match message_tx {
            Some(tx) => tx,
            None => return Err(Error::Transport("Message sender not available".to_string())),
        };

        // Create service function for handling requests
        let container_id_clone = container_id.clone();
        let message_tx_clone = message_tx.clone();
        
        let make_svc = make_service_fn(move |_| {
            let container_id = container_id_clone.clone();
            let message_tx = message_tx_clone.clone();
            
            async move {
                Ok::<_, hyper::Error>(service_fn(move |req: Request<Body>| {
                    let container_id = container_id.clone();
                    let message_tx = message_tx.clone();
                    
                    async move {
                        // Read the request body
                        let body_bytes = match hyper::body::to_bytes(req.into_body()).await {
                            Ok(bytes) => bytes,
                            Err(e) => {
                                eprintln!("Error reading request body: {}", e);
                                return Ok::<_, hyper::Error>(Response::builder()
                                    .status(StatusCode::INTERNAL_SERVER_ERROR)
                                    .body(Body::from(format!("Error: {}", e)))
                                    .unwrap());
                            }
                        };
                        
                        let body_str = String::from_utf8_lossy(&body_bytes);
                        
                        // Parse the SSE message
                        let sse_message = match StdioTransport::parse_sse_message(&body_str) {
                            Ok(msg) => msg,
                            Err(e) => {
                                eprintln!("Error parsing SSE message: {}", e);
                                return Ok(Response::builder()
                                    .status(StatusCode::BAD_REQUEST)
                                    .body(Body::from(format!("Error: {}", e)))
                                    .unwrap());
                            }
                        };
                        
                        // Log the message
                        println!("Received SSE message for container {}: event={}, data={}",
                            container_id,
                            sse_message.event,
                            sse_message.data
                        );
                        
                        // Send the message to the processor
                        if let Err(e) = message_tx.send(sse_message).await {
                            eprintln!("Error sending message: {}", e);
                            return Ok(Response::builder()
                                .status(StatusCode::INTERNAL_SERVER_ERROR)
                                .body(Body::from(format!("Error: {}", e)))
                                .unwrap());
                        }
                        
                        // Return a success response
                        Ok(Response::builder()
                            .status(StatusCode::OK)
                            .body(Body::empty())
                            .unwrap())
                    }
                }))
            }
        });

        // Create the server
        let addr = SocketAddr::from(([0, 0, 0, 0], port));
        let server = Server::bind(&addr).serve(make_svc);
        
        println!("Reverse proxy started for STDIO container {} on port {}", container_name, port);
        println!("Forwarding SSE events to container's stdin/stdout");

        // Create shutdown channel
        let (tx, rx) = oneshot::channel::<()>();
        {
            let mut guard = self.http_shutdown_tx.lock().await;
            *guard = Some(tx);
        }

        // Run the server with graceful shutdown
        let server_with_shutdown = server.with_graceful_shutdown(async {
            rx.await.ok();
        });

        // Spawn the server task
        tokio::spawn(async move {
            if let Err(e) = server_with_shutdown.await {
                eprintln!("Proxy server error: {}", e);
            }
        });

        Ok(())
    }

    /// Process SSE messages and handle bidirectional communication with the container
    async fn process_messages(
        container_id: String,
        runtime: Arc<Mutex<Option<Box<dyn ContainerRuntime>>>>,
        mut message_rx: mpsc::Receiver<SseMessage>,
        response_tx: mpsc::Sender<JsonRpcMessage>,
        mut shutdown_rx: oneshot::Receiver<()>,
    ) -> Result<()> {
        // Get the container runtime and attach to the container
        let (stdin, stdout) = {
            let mut runtime_guard = runtime.lock().await;
            let runtime_ref = runtime_guard.as_mut().ok_or_else(|| {
                Error::Transport("Container runtime not available".to_string())
            })?;
            
            // Attach to the container
            runtime_ref.attach_container(&container_id).await?
        };
        
        let mut stdin = stdin;
        let mut stdout = stdout;
        
        // Create a buffer for reading from stdout
        let mut buffer = Vec::new();
        let mut line_buffer = String::new();
        
        // Spawn a task to read from stdout
        let response_tx_clone = response_tx.clone();
        let stdout_task = tokio::spawn(async move {
            let mut buf = [0u8; 1024];
            loop {
                match stdout.read(&mut buf).await {
                    Ok(0) => {
                        // EOF, container process has terminated
                        println!("Container process terminated");
                        break;
                    }
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
                                                // Send the response
                                                if let Err(e) = response_tx_clone.send(json_rpc).await {
                                                    eprintln!("Failed to send response: {}", e);
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
                        eprintln!("Error reading from container stdout: {}", e);
                        break;
                    }
                }
            }
        });
        
        // Process messages until shutdown signal is received
        loop {
            tokio::select! {
                // Check for shutdown signal
                _ = &mut shutdown_rx => {
                    println!("STDIO transport shutting down");
                    // Cancel the stdout task
                    stdout_task.abort();
                    break;
                }
                
                // Process incoming SSE messages
                Some(sse_message) = message_rx.recv() => {
                    // Convert SSE message to JSON-RPC
                    let json_rpc = Self::sse_to_json_rpc(&sse_message);
                    
                    // Serialize to JSON
                    let json_str = match serde_json::to_string(&json_rpc) {
                        Ok(s) => s,
                        Err(e) => {
                            eprintln!("Failed to serialize JSON-RPC: {}", e);
                            continue;
                        }
                    };
                    
                    // Add newline to ensure proper message separation
                    let json_str = format!("{}\n", json_str);
                    
                    // Log the message for debugging
                    println!("Sending message to container: {}", json_str);
                    
                    // Write to container stdin
                    if let Err(e) = stdin.write_all(json_str.as_bytes()).await {
                        eprintln!("Failed to write to container stdin: {}", e);
                        break;
                    }
                    
                    // Flush stdin to ensure the message is sent
                    if let Err(e) = stdin.flush().await {
                        eprintln!("Failed to flush container stdin: {}", e);
                        break;
                    }
                }
            }
        }
        
        Ok(())
    }

    /// Convert an SSE message to JSON-RPC
    fn sse_to_json_rpc(sse: &SseMessage) -> JsonRpcMessage {
        // Parse the data as JSON if possible
        let params = match serde_json::from_str::<serde_json::Value>(&sse.data) {
            Ok(value) => value,
            Err(_) => serde_json::Value::String(sse.data.clone()),
        };
        
        // Create a JSON-RPC message
        JsonRpcMessage {
            jsonrpc: "2.0".to_string(),
            method: sse.event.clone(),
            params,
            id: sse.id.clone().map(|id| serde_json::Value::String(id)),
        }
    }

    /// Convert a JSON-RPC message to SSE
    fn json_rpc_to_sse(json_rpc: &JsonRpcMessage) -> SseMessage {
        // Convert the params to a string
        let data = serde_json::to_string(&json_rpc.params).unwrap_or_default();
        
        // Create an SSE message
        SseMessage {
            event: json_rpc.method.clone(),
            data,
            id: json_rpc.id.as_ref().and_then(|id| {
                if let serde_json::Value::String(s) = id {
                    Some(s.clone())
                } else {
                    Some(id.to_string())
                }
            }),
        }
    }

    /// Handle an HTTP request and convert it to an SSE message
    pub async fn handle_request(&self, req: Request<Body>) -> Result<Response<Body>> {
        // Get the message sender
        let message_tx = {
            let guard = self.message_tx.lock().await;
            guard.clone()
        };
        
        let message_tx = match message_tx {
            Some(tx) => tx,
            None => return Err(Error::Transport("Message sender not available".to_string())),
        };
        
        // Read the request body
        let body_bytes = hyper::body::to_bytes(req.into_body()).await?;
        let body_str = String::from_utf8_lossy(&body_bytes);
        
        // Parse the SSE message
        let sse_message = Self::parse_sse_message(&body_str)?;
        
        // Send the message to the processor
        if let Err(e) = message_tx.send(sse_message).await {
            return Err(Error::Transport(format!("Failed to send message: {}", e)));
        }
        
        // Get a response if available
        let mut rx_option = self.response_rx.lock().await;
        if let Some(mut rx) = rx_option.take() {
            if let Some(json_rpc) = rx.recv().await {
                // Convert to SSE
                let sse = Self::json_rpc_to_sse(&json_rpc);
                
                // Format as SSE
                let sse_str = format!(
                    "event: {}\ndata: {}\n{}\n",
                    sse.event,
                    sse.data,
                    sse.id.map(|id| format!("id: {}", id)).unwrap_or_default()
                );
                
                // Return the response
                return Ok(Response::builder()
                    .status(StatusCode::OK)
                    .header("Content-Type", "text/event-stream")
                    .body(Body::from(sse_str))
                    .unwrap());
            }
            
            // Put the receiver back
            *rx_option = Some(rx);
        }
        
        // Return a success response
        Ok(Response::builder()
            .status(StatusCode::OK)
            .body(Body::empty())
            .unwrap())
    }

    /// Parse an SSE message from a string
    fn parse_sse_message(message: &str) -> Result<SseMessage> {
        let mut event_type = "message".to_string();
        let mut data = String::new();
        let mut id = None;
        
        for line in message.lines() {
            if line.starts_with("event:") {
                event_type = line[6..].trim().to_string();
            } else if line.starts_with("data:") {
                data = line[5..].trim().to_string();
            } else if line.starts_with("id:") {
                id = Some(line[3..].trim().to_string());
            }
        }
        
        Ok(SseMessage {
            event: event_type,
            data,
            id,
        })
    }
}

#[async_trait]
impl Transport for StdioTransport {
    fn mode(&self) -> TransportMode {
        TransportMode::STDIO
    }

    async fn setup(
        &self,
        container_id: &str,
        container_name: &str,
        port: Option<u16>,
        env_vars: &mut HashMap<String, String>,
    ) -> Result<()> {
        // Store container ID and name
        let mut id_guard = self.container_id.lock().await;
        *id_guard = Some(container_id.to_string());
        drop(id_guard);
        
        let mut name_guard = self.container_name.lock().await;
        *name_guard = Some(container_name.to_string());
        drop(name_guard);

        // Store port if provided
        if let Some(p) = port {
            // Since we're using a non-mutex field, we need to create a mutable clone
            let mut this = self.clone();
            this.port = Some(p);
        }

        // Set environment variables for the container
        env_vars.insert("MCP_TRANSPORT".to_string(), "stdio".to_string());

        Ok(())
    }

    async fn start(&self) -> Result<()> {
        // Get container ID and name
        let container_id = {
            let guard = self.container_id.lock().await;
            guard.clone()
        };
        
        let container_id = match container_id {
            Some(id) => id,
            None => return Err(Error::Transport("Container ID not set".to_string())),
        };

        let container_name = {
            let guard = self.container_name.lock().await;
            guard.clone()
        };
        
        let container_name = match container_name {
            Some(name) => name,
            None => return Err(Error::Transport("Container name not set".to_string())),
        };

        // Create message channels
        let (message_tx, message_rx) = mpsc::channel::<SseMessage>(100);
        {
            let mut guard = self.message_tx.lock().await;
            *guard = Some(message_tx);
        }

        let (response_tx, response_rx) = mpsc::channel::<JsonRpcMessage>(100);
        {
            let mut guard = self.response_rx.lock().await;
            *guard = Some(response_rx);
        }

        // Create shutdown channel
        let (shutdown_tx, shutdown_rx) = oneshot::channel::<()>();
        {
            let mut guard = self.shutdown_tx.lock().await;
            *guard = Some(shutdown_tx);
        }

        // Start the message processor
        let container_id_clone = container_id.clone();
        let runtime_clone = self.runtime.clone();
        
        tokio::spawn(async move {
            if let Err(e) = Self::process_messages(
                container_id_clone,
                runtime_clone,
                message_rx,
                response_tx,
                shutdown_rx,
            ).await {
                eprintln!("Error processing messages: {}", e);
            }
        });

        println!("STDIO transport started for container {}", container_name);

        // Start the HTTP server if a port is provided
        if let Some(port) = self.port {
            if let Err(e) = self.start_http_server(port).await {
                eprintln!("Failed to start HTTP server: {}", e);
                // Continue anyway, as the STDIO transport is still functional
            }
        } else {
            println!("No port specified, HTTP reverse proxy not started");
            println!("To use HTTP reverse proxy with STDIO transport, specify a port with --port");
        }

        Ok(())
    }

    async fn stop(&self) -> Result<()> {
        // Send shutdown signal if available
        let mut guard = self.shutdown_tx.lock().await;
        if let Some(tx) = guard.take() {
            let _ = tx.send(());
            println!("STDIO transport stopped");
        }

        // Stop the HTTP server if it's running
        let mut http_guard = self.http_shutdown_tx.lock().await;
        if let Some(tx) = http_guard.take() {
            let _ = tx.send(());
            println!("HTTP reverse proxy stopped");
        }

        Ok(())
    }

    async fn is_running(&self) -> Result<bool> {
        // Check if shutdown channel is still available
        let stdio_running = self.shutdown_tx.lock().await.is_some();
        let http_running = self.http_shutdown_tx.lock().await.is_some();
        
        // Transport is considered running if either the STDIO transport or HTTP server is running
        Ok(stdio_running || http_running)
    }
    
    fn as_any(&self) -> &dyn Any {
        self
    }
}

/// SSE message structure
#[derive(Debug, Clone)]
pub struct SseMessage {
    pub event: String,
    pub data: String,
    pub id: Option<String>,
}

/// JSON-RPC message structure
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcMessage {
    pub jsonrpc: String,
    pub method: String,
    pub params: serde_json::Value,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<serde_json::Value>,
}

/// Dummy container runtime for initialization
struct DummyContainerRuntime;

#[async_trait]
impl ContainerRuntime for DummyContainerRuntime {
    async fn create_and_start_container(
        &self,
        _image: &str,
        _name: &str,
        _command: Vec<String>,
        _env_vars: HashMap<String, String>,
        _labels: HashMap<String, String>,
        _permission_config: crate::permissions::profile::ContainerPermissionConfig,
    ) -> Result<String> {
        Err(Error::Transport("Dummy runtime not implemented".to_string()))
    }

    async fn list_containers(&self) -> Result<Vec<crate::container::ContainerInfo>> {
        Err(Error::Transport("Dummy runtime not implemented".to_string()))
    }

    async fn stop_container(&self, _container_id: &str) -> Result<()> {
        Err(Error::Transport("Dummy runtime not implemented".to_string()))
    }

    async fn remove_container(&self, _container_id: &str) -> Result<()> {
        Err(Error::Transport("Dummy runtime not implemented".to_string()))
    }

    async fn container_logs(&self, _container_id: &str) -> Result<String> {
        Err(Error::Transport("Dummy runtime not implemented".to_string()))
    }

    async fn is_container_running(&self, _container_id: &str) -> Result<bool> {
        Err(Error::Transport("Dummy runtime not implemented".to_string()))
    }

    async fn get_container_info(&self, _container_id: &str) -> Result<crate::container::ContainerInfo> {
        Err(Error::Transport("Dummy runtime not implemented".to_string()))
    }

    async fn attach_container(&self, _container_id: &str) -> Result<(Box<dyn tokio::io::AsyncWrite + Unpin + Send>, Box<dyn tokio::io::AsyncRead + Unpin + Send>)> {
        Err(Error::Transport("Dummy runtime not implemented".to_string()))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use mockall::predicate::*;
    use mockall::*;
    use crate::container::ContainerInfo;
    use crate::permissions::profile::ContainerPermissionConfig;

    // Mock for testing
    mock! {
        pub ContainerRuntime {}

        #[async_trait]
        impl ContainerRuntime for ContainerRuntime {
            async fn create_and_start_container(
                &self,
                image: &str,
                name: &str,
                command: Vec<String>,
                env_vars: HashMap<String, String>,
                labels: HashMap<String, String>,
                permission_config: ContainerPermissionConfig,
            ) -> Result<String>;

            async fn list_containers(&self) -> Result<Vec<ContainerInfo>>;
            async fn stop_container(&self, container_id: &str) -> Result<()>;
            async fn remove_container(&self, container_id: &str) -> Result<()>;
            async fn container_logs(&self, container_id: &str) -> Result<String>;
            async fn is_container_running(&self, container_id: &str) -> Result<bool>;
            async fn get_container_info(&self, container_id: &str) -> Result<ContainerInfo>;
            async fn attach_container(&self, container_id: &str) -> Result<(Box<dyn tokio::io::AsyncWrite + Unpin + Send>, Box<dyn tokio::io::AsyncRead + Unpin + Send>)>;
        }
    }

    #[tokio::test]
    async fn test_stdio_transport_setup() {
        let transport = StdioTransport::new();
        let mut env_vars = HashMap::new();
        
        transport.setup("test-id", "test-container", None, &mut env_vars).await.unwrap();
        
        assert_eq!(env_vars.get("MCP_TRANSPORT").unwrap(), "stdio");
    }

    #[tokio::test]
    async fn test_stdio_transport_start_without_setup() {
        let transport = StdioTransport::new();
        let result = transport.start().await;
        
        assert!(result.is_err());
    }
}