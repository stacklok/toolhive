use async_trait::async_trait;
use hyper::{Body, Request, Response, Server, StatusCode};
use hyper::service::{make_service_fn, service_fn};
use serde::{Deserialize, Serialize};
use std::any::Any;
use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::Arc;
use std::time::SystemTime;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::sync::{mpsc, oneshot, Mutex};
use uuid::Uuid;

use crate::container::ContainerRuntime;
use crate::error::{Error, Result};
use crate::transport::{Transport, TransportMode};

/// SSE client connection
struct SseClient {
    tx: mpsc::Sender<String>,
    #[allow(dead_code)]
    created_at: SystemTime,
}

/// Message type for pending SSE messages
#[derive(Clone)]
struct PendingMessage {
    event_type: String,
    data: String,
    // None means broadcast to all clients
    target_client_id: Option<Uuid>,
}

/// STDIO transport handler for MCP servers
#[derive(Clone)]
pub struct StdioTransport {
    port: u16,
    container_id: Arc<Mutex<Option<String>>>,
    container_name: Arc<Mutex<Option<String>>>,
    runtime: Arc<Mutex<Option<Box<dyn ContainerRuntime>>>>,
    shutdown_tx: Arc<Mutex<Option<oneshot::Sender<()>>>>,
    message_tx: Arc<Mutex<Option<mpsc::Sender<JsonRpcMessage>>>>,
    response_rx: Arc<Mutex<Option<mpsc::Receiver<JsonRpcMessage>>>>,
    http_shutdown_tx: Arc<Mutex<Option<oneshot::Sender<()>>>>,
    sse_clients: Arc<Mutex<HashMap<Uuid, SseClient>>>,
    // Queue for messages that need to be forwarded to SSE clients
    pending_messages: Arc<Mutex<Vec<PendingMessage>>>,
}

impl StdioTransport {
    /// Create a new STDIO transport handler with a specific port
    pub fn new(port: u16) -> Self {
        Self {
            port,
            container_id: Arc::new(Mutex::new(None)),
            container_name: Arc::new(Mutex::new(None)),
            runtime: Arc::new(Mutex::new(None)),
            shutdown_tx: Arc::new(Mutex::new(None)),
            message_tx: Arc::new(Mutex::new(None)),
            response_rx: Arc::new(Mutex::new(None)),
            http_shutdown_tx: Arc::new(Mutex::new(None)),
            sse_clients: Arc::new(Mutex::new(HashMap::new())),
            pending_messages: Arc::new(Mutex::new(Vec::new())),
        }
    }

    /// Send an SSE event to all connected clients
    async fn send_sse_event(&self, event_type: &str, data: &str) -> Result<()> {
        let clients = self.sse_clients.lock().await;
        
        if clients.is_empty() {
            println!("DEBUG: No SSE clients connected, skipping event");
            log::debug!("No SSE clients connected, skipping event");
            return Ok(());
        }
        
        println!("DEBUG: Sending SSE event to {} clients", clients.len());
        
        for (client_id, client) in clients.iter() {
            let event = format!("event: {}\ndata: {}\n\n", event_type, data);
            println!("DEBUG: Sending event to client {}: {}", client_id, event);
            
            if let Err(e) = client.tx.send(event).await {
                println!("DEBUG: Failed to send SSE event to client {}: {}", client_id, e);
                log::error!("Failed to send SSE event to client {}: {}", client_id, e);
                // We don't remove the client here to avoid modifying the HashMap while iterating
                // Failed clients will be removed when their connection is closed
            } else {
                println!("DEBUG: Successfully sent SSE event to client {}: {}", client_id, event_type);
                log::debug!("Sent SSE event to client {}: {}", client_id, event_type);
            }
        }
        
        Ok(())
    }

    /// Handle an SSE connection
    async fn handle_sse_connection(&self, req: Request<Body>) -> Result<Response<Body>> {
        println!("DEBUG: handle_sse_connection called");
        
        // Extract query for debugging
        let uri = req.uri();
        let query = uri.query().unwrap_or("");
        println!("DEBUG: SSE connection query: {}", query);
        
        // Generate a unique client ID
        let client_id = Uuid::new_v4();
        println!("DEBUG: Generated client ID for SSE: {}", client_id);
        
        // Create a channel for sending events to this client
        let (mut sender, body) = Body::channel();
        
        // Store the client with a channel for sending events
        let (tx, mut rx) = mpsc::channel::<String>(100);
        {
            let mut clients = self.sse_clients.lock().await;
            clients.insert(client_id, SseClient {
                tx: tx.clone(),
                created_at: SystemTime::now(),
            });
            println!("DEBUG: Registered SSE client. Active sessions: {:?}",
                     clients.keys().map(|k| k.to_string()).collect::<Vec<_>>());
        }
        
        // Process any pending messages for this client
        let messages_to_send = {
            let mut pending = self.pending_messages.lock().await;
            if !pending.is_empty() {
                println!("DEBUG: Processing {} pending messages for new client", pending.len());
                
                // Find messages for this client (targeted or broadcast)
                let mut messages_for_client = Vec::new();
                let mut remaining_messages = Vec::new();
                
                for msg in pending.drain(..) {
                    // If the message is for all clients or specifically for this client
                    if msg.target_client_id.is_none() || msg.target_client_id == Some(client_id) {
                        messages_for_client.push(msg);
                    } else {
                        // Keep messages for other clients
                        remaining_messages.push(msg);
                    }
                }
                
                // Put back messages for other clients
                *pending = remaining_messages;
                
                println!("DEBUG: Found {} messages for client {}", messages_for_client.len(), client_id);
                messages_for_client
            } else {
                println!("DEBUG: No pending messages to process");
                Vec::new()
            }
        };
        
        // Send the relevant messages to this client
        for msg in messages_to_send {
            let event = format!("event: {}\ndata: {}\n\n", msg.event_type, msg.data);
            if let Err(e) = tx.send(event).await {
                println!("DEBUG: Failed to send pending message to client {}: {}", client_id, e);
                log::error!("Failed to send pending message to client {}: {}", client_id, e);
            } else {
                println!("DEBUG: Successfully sent pending message to client {}", client_id);
            }
        }
        
        // Create a response with SSE headers
        let response = Response::builder()
            .status(StatusCode::OK)
            .header("Content-Type", "text/event-stream")
            .header("Cache-Control", "no-cache")
            .header("Connection", "keep-alive")
            .body(body)
            .unwrap();
        
        // Get the base URL for the POST endpoint
        let host = req.headers().get("Host").and_then(|h| h.to_str().ok()).unwrap_or("localhost");
        let scheme = req.headers().get("X-Forwarded-Proto").and_then(|h| h.to_str().ok()).unwrap_or("http");
        let base_url = format!("{}://{}", scheme, host);
        
        // Send the endpoint event
        let endpoint_url = format!("{}/mcp/v1/jsonrpc?session_id={}", base_url, client_id);
        println!("DEBUG: Sending endpoint URL: {}", endpoint_url);
        let endpoint_event = format!("event: endpoint\ndata: {}\n\n", endpoint_url);
        
        // Send the initial event directly to the body
        if let Err(e) = sender.send_data(endpoint_event.into()).await {
            log::error!("Failed to send endpoint event to client {}: {}", client_id, e);
            return Err(Error::Transport(format!("Failed to send endpoint event: {}", e)));
        }
        
        // Spawn a task to forward events from the channel to the body
        let client_id_clone = client_id.clone();
        tokio::spawn(async move {
            while let Some(event) = rx.recv().await {
                if let Err(e) = sender.send_data(event.into()).await {
                    log::error!("Failed to send event to client {}: {}", client_id_clone, e);
                    break;
                }
            }
        });
        
        // Log the connection
        log::debug!("SSE client {} connected", client_id);
        
        Ok(response)
    }

    /// Handle a POST request with a JSON-RPC message
    async fn handle_post_request(&self, req: Request<Body>) -> Result<Response<Body>> {
        // Extract session ID from query parameters
        let uri = req.uri();
        let query = uri.query().unwrap_or("");
        println!("DEBUG: Query string: {}", query);
        
        let session_id = query.split('&')
            .find_map(|param| {
                let parts: Vec<&str> = param.split('=').collect();
                if parts.len() == 2 && parts[0] == "session_id" {
                    println!("DEBUG: Found session_id param: {}", parts[1]);
                    let parsed = Uuid::parse_str(parts[1]);
                    if let Err(e) = &parsed {
                        println!("DEBUG: Failed to parse UUID: {}", e);
                    }
                    parsed.ok()
                } else {
                    None
                }
            });
        
        let session_id = match session_id {
            Some(id) => {
                println!("DEBUG: Valid session ID: {}", id);
                id
            },
            None => {
                println!("DEBUG: Missing or invalid session_id");
                return Ok(Response::builder()
                    .status(StatusCode::BAD_REQUEST)
                    .body(Body::from("session_id is required"))
                    .unwrap());
            }
        };
        
        // Check if the session exists
        let client_exists = {
            let clients = self.sse_clients.lock().await;
            println!("DEBUG: Checking if session exists. Active sessions: {:?}",
                     clients.keys().map(|k| k.to_string()).collect::<Vec<_>>());
            clients.contains_key(&session_id)
        };
        
        if !client_exists {
            println!("DEBUG: Session not found: {}", session_id);
            return Ok(Response::builder()
                .status(StatusCode::NOT_FOUND)
                .body(Body::from("Could not find session"))
                .unwrap());
        }
        
        println!("DEBUG: Session found: {}", session_id);
        
        // Read the request body
        let body_bytes = hyper::body::to_bytes(req.into_body()).await
            .map_err(|e| Error::Transport(format!("Error reading request body: {}", e)))?;
        
        let body_str = String::from_utf8_lossy(&body_bytes);
        
        // Parse the JSON-RPC message
        let json_rpc_message = match Self::parse_json_rpc_message(&body_str) {
            Ok(msg) => msg,
            Err(e) => {
                return Ok(Response::builder()
                    .status(StatusCode::BAD_REQUEST)
                    .body(Body::from(format!("Error: {}", e)))
                    .unwrap());
            }
        };
        
        // Log the message
        Self::log_json_rpc_message(&json_rpc_message);
        
        // Get the message sender
        let message_tx = {
            let guard = self.message_tx.lock().await;
            guard.clone()
        };
        
        let message_tx = match message_tx {
            Some(tx) => tx,
            None => return Ok(Response::builder()
                .status(StatusCode::INTERNAL_SERVER_ERROR)
                .body(Body::from("Message sender not available"))
                .unwrap()),
        };
        
        // Send the message to the processor
        if let Err(e) = message_tx.send(json_rpc_message).await {
            return Ok(Response::builder()
                .status(StatusCode::INTERNAL_SERVER_ERROR)
                .body(Body::from(format!("Error: {}", e)))
                .unwrap());
        }
        
        // Return a success response immediately
        println!("DEBUG: Returning ACCEPTED response for POST request");
        Ok(Response::builder()
            .status(StatusCode::ACCEPTED)
            .body(Body::from("Accepted"))
            .unwrap())
    }

    /// Set the container runtime
    pub fn with_runtime(mut self, runtime: Box<dyn ContainerRuntime>) -> Self {
        // Create a new runtime with the provided runtime
        let new_runtime = Arc::new(Mutex::new(Some(runtime)));
        self.runtime = new_runtime;
        self
    }

    /// Start the HTTP server for the HTTP with SSE transport
    async fn start_http_server(&self, port: u16) -> Result<()> {
        // Get container ID and name
        let container_id = {
            let guard = self.container_id.lock().await;
            guard.clone()
        };
        
        if container_id.is_none() {
            return Err(Error::Transport("Container ID not set".to_string()));
        }

        let container_name = {
            let guard = self.container_name.lock().await;
            guard.clone()
        };
        
        let container_name = match container_name {
            Some(name) => name,
            None => return Err(Error::Transport("Container name not set".to_string())),
        };

        // Clone self for use in the service
        let transport = self.clone();

        // Create service function for handling requests
        let make_svc = make_service_fn(move |_| {
            let transport = transport.clone();
            
            async move {
                Ok::<_, hyper::Error>(service_fn(move |req: Request<Body>| {
                    let transport = transport.clone();
                    
                    async move {
                        // Route based on the request path and method
                        let path = req.uri().path();
                        let method = req.method();
                        
                        println!("DEBUG: Received request: {} {}", method, path);
                        
                        match (method, path) {
                            // SSE endpoint
                            (&hyper::Method::GET, "/mcp/v1/events") => {
                                println!("DEBUG: Routing to SSE endpoint");
                                match transport.handle_sse_connection(req).await {
                                    Ok(response) => Ok::<_, hyper::Error>(response),
                                    Err(e) => {
                                        log::error!("Error handling SSE connection: {}", e);
                                        Ok(Response::builder()
                                            .status(StatusCode::INTERNAL_SERVER_ERROR)
                                            .body(Body::from(format!("Error: {}", e)))
                                            .unwrap())
                                    }
                                }
                            },
                            // JSON-RPC endpoint
                            (&hyper::Method::POST, "/mcp/v1/jsonrpc") => {
                                match transport.handle_post_request(req).await {
                                    Ok(response) => Ok::<_, hyper::Error>(response),
                                    Err(e) => {
                                        log::error!("Error handling POST request: {}", e);
                                        Ok(Response::builder()
                                            .status(StatusCode::INTERNAL_SERVER_ERROR)
                                            .body(Body::from(format!("Error: {}", e)))
                                            .unwrap())
                                    }
                                }
                            },
                            // Not found for other paths
                            _ => {
                                Ok(Response::builder()
                                    .status(StatusCode::NOT_FOUND)
                                    .body(Body::from("Not found"))
                                    .unwrap())
                            }
                        }
                    }
                }))
            }
        });

        // Create the server
        let addr = SocketAddr::from(([0, 0, 0, 0], port));
        let server = Server::bind(&addr).serve(make_svc);
        
        log::debug!("HTTP with SSE transport started for STDIO container {} on port {}", container_name, port);
        log::debug!("SSE endpoint: http://localhost:{}/mcp/v1/events", port);
        log::debug!("JSON-RPC endpoint: http://localhost:{}/mcp/v1/jsonrpc", port);

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
                log::error!("HTTP server error: {}", e);
            }
        });

        Ok(())
    }

    /// Forward a JSON-RPC message to SSE clients
    async fn forward_to_sse_clients(&self, json_rpc: &JsonRpcMessage) -> Result<()> {
        println!("DEBUG: Forwarding JSON-RPC message to SSE clients: {:?}", json_rpc);
        
        // Serialize the message to JSON
        let json_str = serde_json::to_string(json_rpc)
            .map_err(|e| Error::Transport(format!("Failed to serialize JSON-RPC: {}", e)))?;
        
        println!("DEBUG: Serialized JSON-RPC message: {}", json_str);
        
        // Check if there are any connected clients
        let has_clients = {
            let clients = self.sse_clients.lock().await;
            !clients.is_empty()
        };
        
        if has_clients {
            // Send the message as an SSE event
            self.send_sse_event("message", &json_str).await?;
            println!("DEBUG: Successfully forwarded JSON-RPC message to SSE clients");
        } else {
            // Queue the message for later delivery
            println!("DEBUG: No SSE clients connected, queueing message for later delivery");
            let mut pending = self.pending_messages.lock().await;
            pending.push(PendingMessage {
                event_type: "message".to_string(),
                data: json_str.clone(),
                target_client_id: None, // Broadcast to all clients
            });
            println!("DEBUG: Message queued. Queue size: {}", pending.len());
        }
        
        log::debug!("Forwarded JSON-RPC message to SSE clients");
        
        Ok(())
    }

    /// Process container stdout data and parse JSON-RPC messages
    async fn process_stdout(
        transport: Arc<Self>,
        mut stdout: Box<dyn tokio::io::AsyncRead + Unpin + Send>,
        response_tx: mpsc::Sender<JsonRpcMessage>,
    ) {
        let mut buffer = Vec::new();
        let mut line_buffer = String::new();
        let mut buf = [0u8; 1024];

        log::debug!("Starting to process container stdout");

        loop {
            log::debug!("Waiting for data from container stdout");
            match stdout.read(&mut buf).await {
                Ok(0) => {
                    // EOF, container process has terminated
                    log::info!("Container process terminated (EOF)");
                    break;
                }
                Ok(n) => {
                    log::debug!("Received {} bytes from container stdout", n);
                    log::debug!("Data: {:?}", String::from_utf8_lossy(&buf[..n]));
                    
                    if let Err(e) = Self::process_stdout_chunk(
                        &transport,
                        &buf[..n],
                        &mut buffer,
                        &mut line_buffer,
                        &response_tx
                    ).await {
                        log::error!("Error processing stdout chunk: {}", e);
                    }
                }
                Err(e) => {
                    log::error!("Error reading from container stdout: {}", e);
                    break;
                }
            }
        }
        
        log::debug!("Finished processing container stdout");
    }

    /// Process a chunk of data from stdout
    async fn process_stdout_chunk(
        transport: &Arc<Self>,
        data: &[u8],
        buffer: &mut Vec<u8>,
        line_buffer: &mut String,
        response_tx: &mpsc::Sender<JsonRpcMessage>,
    ) -> Result<()> {
        // Process the data
        buffer.extend_from_slice(data);

        log::debug!("Processing {} bytes from container stdout", data.len());
        log::debug!("Data: {:?}", String::from_utf8_lossy(data));

        // Process complete lines
        let mut start_idx = 0;
        for i in 0..buffer.len() {
            if buffer[i] == b'\n' {
                // Extract the line
                if let Ok(line) = std::str::from_utf8(&buffer[start_idx..i]) {
                    log::debug!("Found complete line: {}", line);
                    line_buffer.push_str(line);
                    
                    // Try to parse as JSON-RPC
                    if !line_buffer.trim().is_empty() {
                        log::debug!("Attempting to parse line as JSON-RPC: {}", line_buffer);
                        Self::parse_and_forward_json_rpc(transport, line_buffer, response_tx).await;
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
            let remaining = &buffer[start_idx..];
            log::debug!("Keeping partial line in buffer: {:?}", String::from_utf8_lossy(remaining));
            *buffer = remaining.to_vec();
        } else {
            buffer.clear();
            log::debug!("Buffer cleared, no partial line");
        }

        Ok(())
    }

    /// Parse a line as JSON-RPC, forward it to SSE clients, and send it to the response channel
    async fn parse_and_forward_json_rpc(
        transport: &Arc<Self>,
        line: &str,
        response_tx: &mpsc::Sender<JsonRpcMessage>,
    ) {
        log::debug!("Parsing line as JSON-RPC");
        
        match serde_json::from_str::<JsonRpcMessage>(line.trim()) {
            Ok(json_rpc) => {
                log::debug!("Successfully parsed JSON-RPC message");
                
                // Forward to SSE clients
                if let Err(e) = transport.forward_to_sse_clients(&json_rpc).await {
                    log::error!("Failed to forward to SSE clients: {}", e);
                }
                
                // Also send to the response channel for backward compatibility
                if let Err(e) = response_tx.send(json_rpc).await {
                    log::error!("Failed to send response: {}", e);
                } else {
                    log::debug!("Successfully sent JSON-RPC message to response channel");
                }
            }
            Err(e) => {
                log::error!("Failed to parse JSON-RPC message: {}", e);
                log::debug!("Message: {}", line);
            }
        }
    }

    /// Send a JSON-RPC message to the container's stdin
    async fn send_message_to_container(
        stdin: &mut Box<dyn tokio::io::AsyncWrite + Unpin + Send>,
        json_rpc: &JsonRpcMessage,
    ) -> Result<()> {
        // Serialize to JSON
        let json_str = serde_json::to_string(json_rpc)
            .map_err(|e| Error::Transport(format!("Failed to serialize JSON-RPC: {}", e)))?;
        
        // Add a newline to ensure proper message separation
        let json_str = format!("{}\n", json_str);
        
        // Log the message for debugging
        log::debug!("Sending JSON-RPC message to container: {}", json_str);
        
        // Write to container stdin
        stdin.write_all(json_str.as_bytes()).await
            .map_err(|e| Error::Transport(format!("Failed to write to container stdin: {}", e)))?;
        
        // Flush stdin to ensure the message is sent
        stdin.flush().await
            .map_err(|e| Error::Transport(format!("Failed to flush container stdin: {}", e)))?;
        
        Ok(())
    }

    /// Process JSON-RPC messages and handle bidirectional communication with the container
    /// using provided stdin and stdout
    async fn process_messages_with_io(
        mut stdin: Box<dyn tokio::io::AsyncWrite + Unpin + Send>,
        stdout: Box<dyn tokio::io::AsyncRead + Unpin + Send>,
        mut message_rx: mpsc::Receiver<JsonRpcMessage>,
        response_tx: mpsc::Sender<JsonRpcMessage>,
        mut shutdown_rx: oneshot::Receiver<()>,
    ) -> Result<()> {
        // Create a reference to self for the stdout task
        let transport = Arc::new(Self {
            port: 0, // Dummy value, not used in the stdout task
            container_id: Arc::new(Mutex::new(None)),
            container_name: Arc::new(Mutex::new(None)),
            runtime: Arc::new(Mutex::new(None)),
            shutdown_tx: Arc::new(Mutex::new(None)),
            message_tx: Arc::new(Mutex::new(None)),
            response_rx: Arc::new(Mutex::new(None)),
            http_shutdown_tx: Arc::new(Mutex::new(None)),
            sse_clients: Arc::new(Mutex::new(HashMap::new())),
            pending_messages: Arc::new(Mutex::new(Vec::new())),
        });
        
        // Spawn a task to read from stdout
        let response_tx_clone = response_tx.clone();
        let transport_clone = transport.clone();
        let stdout_task = tokio::spawn(async move {
            Self::process_stdout(transport_clone, stdout, response_tx_clone).await;
        });
        
        // Process messages until shutdown signal is received
        loop {
            tokio::select! {
                // Check for shutdown signal
                _ = &mut shutdown_rx => {
                    log::debug!("STDIO transport shutting down");
                    // Cancel the stdout task
                    stdout_task.abort();
                    break;
                }
                
                // Process incoming JSON-RPC messages
                Some(json_rpc_message) = message_rx.recv() => {
                    if let Err(e) = Self::send_message_to_container(&mut stdin, &json_rpc_message).await {
                        log::error!("{}", e);
                        break;
                    }
                }
            }
        }
        
        Ok(())
    }

    /// Log a received JSON-RPC message
    fn log_json_rpc_message(json_rpc: &JsonRpcMessage) {
        // Log the message for debugging
        if json_rpc.is_request() {
            log::debug!("Received JSON-RPC request: method={}, id={:?}",
                json_rpc.method.as_ref().unwrap_or(&"none".to_string()),
                json_rpc.id);
        } else if json_rpc.is_response() {
            log::debug!("Received JSON-RPC response: id={:?}, result={:?}, error={:?}",
                json_rpc.id,
                json_rpc.result.is_some(),
                json_rpc.error.is_some());
        } else if json_rpc.is_notification() {
            log::debug!("Received JSON-RPC notification: method={}",
                json_rpc.method.as_ref().unwrap_or(&"none".to_string()));
        } else {
            log::debug!("Received unknown JSON-RPC message: {:?}", json_rpc);
        }
    }

    /// Handle an HTTP request containing a JSON-RPC message
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
        let body_bytes = hyper::body::to_bytes(req.into_body()).await
            .map_err(|e| Error::Transport(format!("Failed to read request body: {}", e)))?;
        let body_str = String::from_utf8_lossy(&body_bytes);
        
        // Parse the JSON-RPC message
        let json_rpc_message = Self::parse_json_rpc_message(&body_str)?;
        
        // Log the received message
        Self::log_json_rpc_message(&json_rpc_message);
        
        // Send the message to the processor
        if let Err(e) = message_tx.send(json_rpc_message.clone()).await {
            return Err(Error::Transport(format!("Failed to send message: {}", e)));
        }
        
        // Get a response if available
        let mut rx_option = self.response_rx.lock().await;
        if let Some(mut rx) = rx_option.take() {
            if let Some(json_rpc) = rx.recv().await {
                // Serialize the JSON-RPC response
                let json_str = serde_json::to_string(&json_rpc)
                    .map_err(|e| Error::Transport(format!("Failed to serialize JSON-RPC response: {}", e)))?;
                
                // Return the response
                return Ok(Response::builder()
                    .status(StatusCode::OK)
                    .header("Content-Type", "application/json")
                    .body(Body::from(json_str))
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

    /// Parse a JSON-RPC message from a string
    fn parse_json_rpc_message(message: &str) -> Result<JsonRpcMessage> {
        // Parse the JSON-RPC message
        let json_rpc: JsonRpcMessage = serde_json::from_str(message)
            .map_err(|e| Error::Transport(format!("Failed to parse JSON-RPC message: {}", e)))?;
        
        // Validate the message according to the MCP specification
        if json_rpc.jsonrpc != "2.0" {
            return Err(Error::Transport(format!("Invalid JSON-RPC version: {}", json_rpc.jsonrpc)));
        }
        
        // Validate that the message is a valid request, response, or notification
        if json_rpc.is_request() || json_rpc.is_response() || json_rpc.is_notification() {
            Ok(json_rpc)
        } else {
            Err(Error::Transport("Invalid JSON-RPC message format".to_string()))
        }
    }
}

#[async_trait]
impl Transport for StdioTransport {
    fn mode(&self) -> TransportMode {
        TransportMode::STDIO
    }

    fn port(&self) -> u16 {
        self.port
    }

    async fn setup(
        &self,
        container_id: &str,
        container_name: &str,
        _env_vars: &mut HashMap<String, String>,
        _container_ip: Option<String>,
    ) -> Result<()> {
        // Store container ID and name
        let mut id_guard = self.container_id.lock().await;
        *id_guard = Some(container_id.to_string());
        drop(id_guard);
        
        let mut name_guard = self.container_name.lock().await;
        *name_guard = Some(container_name.to_string());
        drop(name_guard);

        Ok(())
    }

    async fn start(
        &self,
        stdin: Option<Box<dyn tokio::io::AsyncWrite + Unpin + Send>>,
        stdout: Option<Box<dyn tokio::io::AsyncRead + Unpin + Send>>,
    ) -> Result<()> {
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

        // Create message channels for JSON-RPC messages
        let (message_tx, message_rx) = mpsc::channel::<JsonRpcMessage>(100);
        {
            let mut guard = self.message_tx.lock().await;
            *guard = Some(message_tx.clone());
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

        // Use provided stdin/stdout if available, otherwise attach to the container
        let (stdin_box, stdout_box) = if let (Some(stdin_provided), Some(stdout_provided)) = (stdin, stdout) {
            log::debug!("Using provided stdin/stdout for container {}", container_name);
            (stdin_provided, stdout_provided)
        } else {
            log::debug!("No stdin/stdout provided, attaching to container {}", container_id);
            return Err(Error::Transport("Stdin/stdout must be provided".to_string()));
        };
        
        tokio::spawn(async move {
            if let Err(e) = Self::process_messages_with_io(
                stdin_box,
                stdout_box,
                message_rx,
                response_tx,
                shutdown_rx,
            ).await {
                log::error!("Error processing messages: {}", e);
            }
        });

        log::debug!("STDIO transport started for container {}", container_name);

        // Start the HTTP server
        if let Err(e) = self.start_http_server(self.port).await {
            log::error!("Failed to start HTTP server: {}", e);
            // Continue anyway, as the STDIO transport is still functional
        }

       // Initialization is handled by the client, not the proxy

        Ok(())
    }

    async fn stop(&self) -> Result<()> {
        // Send shutdown signal if available
        let mut guard = self.shutdown_tx.lock().await;
        if let Some(tx) = guard.take() {
            let _ = tx.send(());
            log::debug!("STDIO transport stopped");
        }

        // Stop the HTTP server if it's running
        let mut http_guard = self.http_shutdown_tx.lock().await;
        if let Some(tx) = http_guard.take() {
            let _ = tx.send(());
            log::debug!("HTTP with SSE transport stopped");
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

/// JSON-RPC error structure
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcError {
    pub code: i32,
    pub message: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<serde_json::Value>,
}

/// JSON-RPC message structure that follows the MCP protocol specification
/// This can represent any of the three message types defined in the MCP spec:
/// 1. Requests: { jsonrpc: "2.0", id: string|number, method: string, params?: object }
/// 2. Responses: { jsonrpc: "2.0", id: string|number, result?: object, error?: { code: number, message: string, data?: any } }
/// 3. Notifications: { jsonrpc: "2.0", method: string, params?: object }
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct JsonRpcMessage {
    pub jsonrpc: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<serde_json::Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub method: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub params: Option<serde_json::Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub result: Option<serde_json::Value>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<JsonRpcError>,
}

impl JsonRpcMessage {
    /// Create a new request message
    pub fn new_request(method: &str, params: Option<serde_json::Value>, id: serde_json::Value) -> Self {
        Self {
            jsonrpc: "2.0".to_string(),
            id: Some(id),
            method: Some(method.to_string()),
            params,
            result: None,
            error: None,
        }
    }

    /// Create a new response message
    pub fn new_response(id: serde_json::Value, result: serde_json::Value) -> Self {
        Self {
            jsonrpc: "2.0".to_string(),
            id: Some(id),
            method: None,
            params: None,
            result: Some(result),
            error: None,
        }
    }

    /// Create a new error response message
    pub fn new_error(id: serde_json::Value, code: i32, message: &str, data: Option<serde_json::Value>) -> Self {
        Self {
            jsonrpc: "2.0".to_string(),
            id: Some(id),
            method: None,
            params: None,
            result: None,
            error: Some(JsonRpcError {
                code,
                message: message.to_string(),
                data,
            }),
        }
    }

    /// Create a new notification message
    pub fn new_notification(method: &str, params: Option<serde_json::Value>) -> Self {
        Self {
            jsonrpc: "2.0".to_string(),
            id: None,
            method: Some(method.to_string()),
            params,
            result: None,
            error: None,
        }
    }

    /// Check if this is a request message
    pub fn is_request(&self) -> bool {
        self.id.is_some() && self.method.is_some()
    }

    /// Check if this is a response message
    pub fn is_response(&self) -> bool {
        self.id.is_some() && (self.result.is_some() || self.error.is_some())
    }

    /// Check if this is a notification message
    pub fn is_notification(&self) -> bool {
        self.id.is_none() && self.method.is_some()
    }
}

/// Dummy container runtime for initialization
#[allow(dead_code)]
struct DummyContainerRuntime;

#[async_trait]
impl ContainerRuntime for DummyContainerRuntime {
    async fn create_container(
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

    async fn start_container(&self, _container_id: &str) -> Result<()> {
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

    async fn get_container_ip(&self, _container_id: &str) -> Result<String> {
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
            async fn create_container(
                &self,
                image: &str,
                name: &str,
                command: Vec<String>,
                env_vars: HashMap<String, String>,
                labels: HashMap<String, String>,
                permission_config: ContainerPermissionConfig,
            ) -> Result<String>;

            async fn start_container(&self, container_id: &str) -> Result<()>;

            async fn list_containers(&self) -> Result<Vec<ContainerInfo>>;
            async fn stop_container(&self, container_id: &str) -> Result<()>;
            async fn remove_container(&self, container_id: &str) -> Result<()>;
            async fn container_logs(&self, container_id: &str) -> Result<String>;
            async fn is_container_running(&self, container_id: &str) -> Result<bool>;
            async fn get_container_info(&self, container_id: &str) -> Result<ContainerInfo>;
            async fn get_container_ip(&self, container_id: &str) -> Result<String>;
            async fn attach_container(&self, container_id: &str) -> Result<(Box<dyn tokio::io::AsyncWrite + Unpin + Send>, Box<dyn tokio::io::AsyncRead + Unpin + Send>)>;
        }
    }

    #[tokio::test]
    async fn test_stdio_transport_setup() {
        let transport = StdioTransport::new(8080);
        let mut env_vars = HashMap::new();
        
        transport.setup("test-id", "test-container", &mut env_vars, None).await.unwrap();
        
        // Environment variables are now set in the environment module, not in the transport
        // So we don't check them here anymore
        
        // Check that container ID and name were set correctly
        let container_id = transport.container_id.lock().await.clone();
        let container_name = transport.container_name.lock().await.clone();
        
        assert_eq!(container_id, Some("test-id".to_string()));
        assert_eq!(container_name, Some("test-container".to_string()));
    }

    #[tokio::test]
    async fn test_stdio_transport_start_without_setup() {
        let transport = StdioTransport::new(8080);
        let result = transport.start(None, None).await;
        
        assert!(result.is_err());
    }
    
    // Helper struct for testing
    struct MockAsyncReadWrite {
        _data: Vec<u8>,
    }
    
    impl MockAsyncReadWrite {
        fn new() -> Self {
            Self { _data: Vec::new() }
        }
    }
    
    impl tokio::io::AsyncRead for MockAsyncReadWrite {
        fn poll_read(
            self: std::pin::Pin<&mut Self>,
            _cx: &mut std::task::Context<'_>,
            _buf: &mut tokio::io::ReadBuf<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            std::task::Poll::Ready(Ok(()))
        }
    }
    
    impl tokio::io::AsyncWrite for MockAsyncReadWrite {
        fn poll_write(
            self: std::pin::Pin<&mut Self>,
            _cx: &mut std::task::Context<'_>,
            buf: &[u8],
        ) -> std::task::Poll<std::io::Result<usize>> {
            std::task::Poll::Ready(Ok(buf.len()))
        }
        
        fn poll_flush(
            self: std::pin::Pin<&mut Self>,
            _cx: &mut std::task::Context<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            std::task::Poll::Ready(Ok(()))
        }
        
        fn poll_shutdown(
            self: std::pin::Pin<&mut Self>,
            _cx: &mut std::task::Context<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            std::task::Poll::Ready(Ok(()))
        }
    }
    
    #[tokio::test]
    async fn test_stdio_transport_lifecycle() -> Result<()> {
        // Create a mock runtime
        let mut mock_runtime = MockContainerRuntime::new();
        
        // Set up expectations for attach_container
        mock_runtime.expect_attach_container()
            .returning(|_| {
                // Create dummy stdin/stdout that implement both AsyncRead and AsyncWrite
                let stdin = MockAsyncReadWrite::new();
                let stdout = MockAsyncReadWrite::new();
                Ok((Box::new(stdin), Box::new(stdout)))
            });
        
        // Create a transport with the mock runtime
        let transport = StdioTransport::new(9001).with_runtime(Box::new(mock_runtime));
        let mut env_vars = HashMap::new();
        
        // Set up the transport
        transport.setup("test-id", "test-container", &mut env_vars, None).await?;
        
        // Start the transport with stdin/stdout
        let stdin = MockAsyncReadWrite::new();
        let stdout = MockAsyncReadWrite::new();
        transport.start(Some(Box::new(stdin)), Some(Box::new(stdout))).await?;
        
        // Check if it's running
        assert!(transport.is_running().await?);
        
        // Stop the transport
        transport.stop().await?;
        
        // Check if it's stopped
        assert!(!transport.is_running().await?);
        
        Ok(())
    }
    
    #[test]
    fn test_json_rpc_message_creation() {
        // Test request message
        let request = JsonRpcMessage::new_request(
            "test-method",
            Some(serde_json::json!({"key": "value", "number": 42})),
            serde_json::Value::String("123".to_string())
        );
        
        assert_eq!(request.jsonrpc, "2.0");
        assert_eq!(request.method, Some("test-method".to_string()));
        assert_eq!(request.params.as_ref().unwrap()["key"], "value");
        assert_eq!(request.params.as_ref().unwrap()["number"], 42);
        assert_eq!(request.id, Some(serde_json::Value::String("123".to_string())));
        assert!(request.is_request());
        assert!(!request.is_response());
        assert!(!request.is_notification());
        
        // Test response message
        let response = JsonRpcMessage::new_response(
            serde_json::Value::String("123".to_string()),
            serde_json::json!({"result": "success"})
        );
        
        assert_eq!(response.jsonrpc, "2.0");
        assert_eq!(response.method, None);
        assert_eq!(response.params, None);
        assert_eq!(response.result.as_ref().unwrap()["result"], "success");
        assert_eq!(response.id, Some(serde_json::Value::String("123".to_string())));
        assert!(!response.is_request());
        assert!(response.is_response());
        assert!(!response.is_notification());
        
        // Test error response message
        let error_response = JsonRpcMessage::new_error(
            serde_json::Value::String("123".to_string()),
            -32600,
            "Invalid Request",
            Some(serde_json::json!({"details": "Method not found"}))
        );
        
        assert_eq!(error_response.jsonrpc, "2.0");
        assert_eq!(error_response.method, None);
        assert_eq!(error_response.params, None);
        assert_eq!(error_response.result, None);
        assert_eq!(error_response.id, Some(serde_json::Value::String("123".to_string())));
        assert_eq!(error_response.error.as_ref().unwrap().code, -32600);
        assert_eq!(error_response.error.as_ref().unwrap().message, "Invalid Request");
        assert_eq!(error_response.error.as_ref().unwrap().data.as_ref().unwrap()["details"], "Method not found");
        assert!(!error_response.is_request());
        assert!(error_response.is_response());
        assert!(!error_response.is_notification());
        
        // Test notification message
        let notification = JsonRpcMessage::new_notification(
            "test-notification",
            Some(serde_json::json!({"event": "update"}))
        );
        
        assert_eq!(notification.jsonrpc, "2.0");
        assert_eq!(notification.method, Some("test-notification".to_string()));
        assert_eq!(notification.params.as_ref().unwrap()["event"], "update");
        assert_eq!(notification.id, None);
        assert!(!notification.is_request());
        assert!(!notification.is_response());
        assert!(notification.is_notification());
    }
    
    #[tokio::test]
    async fn test_stdio_transport_port() -> Result<()> {
        // Create a transport with a port
        let transport = StdioTransport::new(8080);
        
        // Check that the port was set correctly
        assert_eq!(transport.port, 8080);
        
        // Set up the transport
        let mut env_vars = HashMap::new();
        transport.setup("test-id", "test-container", &mut env_vars, None).await?;
        
        // Environment variables are now set in the environment module, not in the transport
        // So we don't check them here anymore
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_stdio_transport_stop_when_not_running() -> Result<()> {
        // Create a transport
        let transport = StdioTransport::new(8080);
        
        // Stop the transport (should not fail even though it's not running)
        transport.stop().await?;
        
        // Check if it's running (should be false)
        assert!(!transport.is_running().await?);
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_stdio_transport_is_running_with_shutdown_tx() -> Result<()> {
        // Create a transport
        let transport = StdioTransport::new(8080);
        
        // Manually set the shutdown_tx to simulate a running transport
        let (tx, _rx) = tokio::sync::oneshot::channel::<()>();
        *transport.shutdown_tx.lock().await = Some(tx);
        
        // Check if it's running (should be true)
        assert!(transport.is_running().await?);
        
        // Stop the transport
        transport.stop().await?;
        
        // Check if it's running (should be false)
        assert!(!transport.is_running().await?);
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_stdio_transport_is_running_with_http_shutdown_tx() -> Result<()> {
        // Create a transport
        let transport = StdioTransport::new(8080);
        
        // Manually set the http_shutdown_tx to simulate a running HTTP server
        let (tx, _rx) = tokio::sync::oneshot::channel::<()>();
        *transport.http_shutdown_tx.lock().await = Some(tx);
        
        // Check if it's running (should be true)
        assert!(transport.is_running().await?);
        
        // Stop the transport
        transport.stop().await?;
        
        // Check if it's running (should be false)
        assert!(!transport.is_running().await?);
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_stdio_transport_handle_request_error() -> Result<()> {
        // Create a transport
        let transport = StdioTransport::new(8080);
        
        // Create a request with invalid JSON-RPC message
        let req = hyper::Request::builder()
            .method("POST")
            .uri("http://localhost:8080/")
            .header("Content-Type", "application/json")
            .body(hyper::Body::from("invalid json"))
            .unwrap();
        
        // Try to handle the request (should fail because message_tx is not set)
        let result = transport.handle_request(req).await;
        
        // Verify the result is an error
        assert!(result.is_err());
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_stdio_transport_handle_request_with_response() -> Result<()> {
        // Create a transport
        let transport = StdioTransport::new(8080);
        
        // Create message channels
        let (message_tx, _message_rx) = mpsc::channel::<JsonRpcMessage>(100);
        *transport.message_tx.lock().await = Some(message_tx);
        
        // Create a JSON-RPC request
        let json_rpc_request = JsonRpcMessage::new_request(
            "test-method",
            Some(serde_json::json!({"key": "value"})),
            serde_json::Value::String("123".to_string())
        );
        
        // Serialize to JSON
        let json_str = serde_json::to_string(&json_rpc_request).unwrap();
        
        // Create a request
        let req = hyper::Request::builder()
            .method("POST")
            .uri("http://localhost:8080/")
            .header("Content-Type", "application/json")
            .body(hyper::Body::from(json_str))
            .unwrap();
        
        // Handle the request (should succeed now that message_tx is set)
        let response = transport.handle_request(req).await?;
        
        // Verify the response
        assert_eq!(response.status(), hyper::StatusCode::OK);
        
        Ok(())
    }
    
    #[test]
    fn test_parse_json_rpc_message() {
        // Valid request
        let request_str = r#"{"jsonrpc":"2.0","id":"1","method":"test","params":{"key":"value"}}"#;
        let request = StdioTransport::parse_json_rpc_message(request_str).unwrap();
        assert!(request.is_request());
        assert_eq!(request.method, Some("test".to_string()));
        assert_eq!(request.id, Some(serde_json::Value::String("1".to_string())));
        
        // Valid response
        let response_str = r#"{"jsonrpc":"2.0","id":"1","result":{"status":"success"}}"#;
        let response = StdioTransport::parse_json_rpc_message(response_str).unwrap();
        assert!(response.is_response());
        assert_eq!(response.result.as_ref().unwrap()["status"], "success");
        
        // Valid error response
        let error_str = r#"{"jsonrpc":"2.0","id":"1","error":{"code":-32600,"message":"Invalid Request"}}"#;
        let error = StdioTransport::parse_json_rpc_message(error_str).unwrap();
        assert!(error.is_response());
        assert_eq!(error.error.as_ref().unwrap().code, -32600);
        
        // Valid notification
        let notification_str = r#"{"jsonrpc":"2.0","method":"update","params":{"status":"complete"}}"#;
        let notification = StdioTransport::parse_json_rpc_message(notification_str).unwrap();
        assert!(notification.is_notification());
        assert_eq!(notification.method, Some("update".to_string()));
        
        // Invalid JSON-RPC version
        let invalid_version = r#"{"jsonrpc":"1.0","id":"1","method":"test"}"#;
        let result = StdioTransport::parse_json_rpc_message(invalid_version);
        assert!(result.is_err());
        
        // Invalid message format (neither request, response, nor notification)
        let invalid_format = r#"{"jsonrpc":"2.0"}"#;
        let result = StdioTransport::parse_json_rpc_message(invalid_format);
        assert!(result.is_err());
    }


    #[tokio::test]
    async fn test_process_stdout_chunk() -> Result<()> {
        // Create a mock transport
        let transport = Arc::new(StdioTransport::new(8080));
        
        // Create test data
        let data = b"test line 1\ntest line 2\n";
        let mut buffer = Vec::new();
        let mut line_buffer = String::new();
        
        // Create a channel for testing
        let (tx, mut rx) = mpsc::channel::<JsonRpcMessage>(10);
        
        // Mock JSON-RPC message for the first line
        let json_rpc1 = r#"{"jsonrpc":"2.0","method":"test","params":{"key":"value"},"id":"1"}"#;
        
        // Process the chunk
        StdioTransport::process_stdout_chunk(
            &transport,
            data,
            &mut buffer,
            &mut line_buffer,
            &tx
        ).await?;
        
        // Buffer should be empty after processing complete lines
        assert!(buffer.is_empty());
        
        // Line buffer should be empty after processing
        assert!(line_buffer.is_empty());
        
        // No messages should be received since our test data isn't valid JSON-RPC
        let timeout = tokio::time::timeout(std::time::Duration::from_millis(100), rx.recv()).await;
        assert!(timeout.is_err()); // Timeout expected
        
        // Now test with valid JSON-RPC
        let data = json_rpc1.as_bytes();
        let mut buffer = Vec::new();
        buffer.extend_from_slice(data);
        buffer.push(b'\n');
        
        // Process the chunk
        StdioTransport::process_stdout_chunk(
            &transport,
            &buffer,
            &mut Vec::new(),
            &mut line_buffer,
            &tx
        ).await?;
        
        // Now we should receive a message
        let received = rx.recv().await;
        assert!(received.is_some());
        let message = received.unwrap();
        assert_eq!(message.method, Some("test".to_string()));
        assert_eq!(message.params.as_ref().unwrap()["key"], "value");
        
        Ok(())
    }

    #[tokio::test]
    async fn test_parse_and_forward_json_rpc() {
        // Create a mock transport
        let transport = Arc::new(StdioTransport::new(8080));
        
        // Create a channel for testing
        let (tx, mut rx) = mpsc::channel::<JsonRpcMessage>(10);
        
        // Valid JSON-RPC message
        let json_rpc = r#"{"jsonrpc":"2.0","method":"test","params":{"key":"value"},"id":"1"}"#;
        
        // Parse and forward
        StdioTransport::parse_and_forward_json_rpc(&transport, json_rpc, &tx).await;
        
        // Check that we received the message
        let received = rx.recv().await;
        assert!(received.is_some());
        let message = received.unwrap();
        assert_eq!(message.jsonrpc, "2.0");
        assert_eq!(message.method, Some("test".to_string()));
        assert_eq!(message.params.as_ref().unwrap()["key"], "value");
        assert_eq!(message.id, Some(serde_json::Value::String("1".to_string())));
        
        // Invalid JSON-RPC message
        let invalid_json = "not a json";
        
        // Parse and forward (should not panic)
        StdioTransport::parse_and_forward_json_rpc(&transport, invalid_json, &tx).await;
        
        // No message should be received
        let timeout = tokio::time::timeout(std::time::Duration::from_millis(100), rx.recv()).await;
        assert!(timeout.is_err()); // Timeout expected
    }

    #[tokio::test]
    async fn test_send_message_to_container() -> Result<()> {
        // Create a mock stdin
        let mut stdin = Box::new(MockAsyncReadWrite::new()) as Box<dyn tokio::io::AsyncWrite + Unpin + Send>;
        
        // Create a JSON-RPC message
        let json_rpc_message = JsonRpcMessage::new_request(
            "test-method",
            Some(serde_json::json!({"key": "value"})),
            serde_json::Value::String("123".to_string())
        );
        
        // Send the message
        let result = StdioTransport::send_message_to_container(&mut stdin, &json_rpc_message).await;
        
        // Check that it succeeded
        assert!(result.is_ok());
        
        Ok(())
    }

    // Additional tests for edge cases and error handling

    #[tokio::test]
    async fn test_process_stdout_chunk_with_partial_lines() -> Result<()> {
        // Create a mock transport
        let transport = Arc::new(StdioTransport::new(8080));
        
        // Create test data with a partial line at the end
        let data = b"complete line 1\npartial line";
        let mut buffer = Vec::new();
        let mut line_buffer = String::new();
        
        // Create a channel for testing
        let (tx, _rx) = mpsc::channel::<JsonRpcMessage>(10);
        
        // Process the chunk
        StdioTransport::process_stdout_chunk(
            &transport,
            data,
            &mut buffer,
            &mut line_buffer,
            &tx
        ).await?;
        
        // Buffer should contain the partial line
        assert_eq!(buffer, b"partial line");
        
        // Line buffer should be empty (since no complete lines were processed)
        assert!(line_buffer.is_empty());
        
        // Now add the rest of the line
        let data2 = b" continued\n";
        
        // Process the second chunk
        StdioTransport::process_stdout_chunk(
            &transport,
            data2,
            &mut buffer,
            &mut line_buffer,
            &tx
        ).await?;
        
        // Buffer should be empty now
        assert!(buffer.is_empty());
        
        // Line buffer should be empty (since it's cleared after processing)
        assert!(line_buffer.is_empty());
        
        Ok(())
    }

    #[tokio::test]
    async fn test_process_stdout_chunk_with_empty_data() -> Result<()> {
        // Create a mock transport
        let transport = Arc::new(StdioTransport::new(8080));
        
        // Create empty test data
        let data = b"";
        let mut buffer = Vec::new();
        let mut line_buffer = String::new();
        
        // Create a channel for testing
        let (tx, _rx) = mpsc::channel::<JsonRpcMessage>(10);
        
        // Process the chunk
        let result = StdioTransport::process_stdout_chunk(
            &transport,
            data,
            &mut buffer,
            &mut line_buffer,
            &tx
        ).await;
        
        // Should succeed with empty data
        assert!(result.is_ok());
        
        // Buffer should still be empty
        assert!(buffer.is_empty());
        
        // Line buffer should be unchanged
        assert!(line_buffer.is_empty());
        
        Ok(())
    }

    #[tokio::test]
    async fn test_process_stdout_chunk_with_multiple_json_messages() -> Result<()> {
        // Create a mock transport
        let transport = Arc::new(StdioTransport::new(8080));
        
        // Create test data with multiple JSON-RPC messages
        let json1 = r#"{"jsonrpc":"2.0","method":"test1","params":{"key":"value1"},"id":"1"}"#;
        let json2 = r#"{"jsonrpc":"2.0","method":"test2","params":{"key":"value2"},"id":"2"}"#;
        let data = format!("{}\n{}\n", json1, json2).into_bytes();
        
        let mut buffer = Vec::new();
        let mut line_buffer = String::new();
        
        // Create a channel for testing
        let (tx, mut rx) = mpsc::channel::<JsonRpcMessage>(10);
        
        // Process the chunk
        StdioTransport::process_stdout_chunk(
            &transport,
            &data,
            &mut buffer,
            &mut line_buffer,
            &tx
        ).await?;
        
        // Buffer should be empty after processing complete lines
        assert!(buffer.is_empty());
        
        // Line buffer should be empty after processing
        assert!(line_buffer.is_empty());
        
        // Should receive two messages
        let msg1 = rx.recv().await.unwrap();
        assert_eq!(msg1.method, Some("test1".to_string()));
        assert_eq!(msg1.params.as_ref().unwrap()["key"], "value1");
        
        let msg2 = rx.recv().await.unwrap();
        assert_eq!(msg2.method, Some("test2".to_string()));
        assert_eq!(msg2.params.as_ref().unwrap()["key"], "value2");
        
        Ok(())
    }

    // Mock that fails on write
    struct FailingAsyncWrite;
    
    impl tokio::io::AsyncWrite for FailingAsyncWrite {
        fn poll_write(
            self: std::pin::Pin<&mut Self>,
            _cx: &mut std::task::Context<'_>,
            _buf: &[u8],
        ) -> std::task::Poll<std::io::Result<usize>> {
            std::task::Poll::Ready(Err(std::io::Error::new(std::io::ErrorKind::Other, "Write error")))
        }
        
        fn poll_flush(
            self: std::pin::Pin<&mut Self>,
            _cx: &mut std::task::Context<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            std::task::Poll::Ready(Ok(()))
        }
        
        fn poll_shutdown(
            self: std::pin::Pin<&mut Self>,
            _cx: &mut std::task::Context<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            std::task::Poll::Ready(Ok(()))
        }
    }

    #[tokio::test]
    async fn test_send_message_to_container_with_write_error() {
        // Create a failing stdin
        let mut stdin = Box::new(FailingAsyncWrite) as Box<dyn tokio::io::AsyncWrite + Unpin + Send>;
        
        // Create a JSON-RPC message
        let json_rpc_message = JsonRpcMessage::new_request(
            "test-method",
            Some(serde_json::json!({"key": "value"})),
            serde_json::Value::String("123".to_string())
        );
        
        // Send the message
        let result = StdioTransport::send_message_to_container(&mut stdin, &json_rpc_message).await;
        
        // Should fail with a transport error
        assert!(result.is_err());
        if let Err(Error::Transport(msg)) = result {
            assert!(msg.contains("Failed to write to container stdin"));
        } else {
            panic!("Expected Transport error");
        }
    }

    // Mock that fails on flush
    struct FailingFlushAsyncWrite;
    
    impl tokio::io::AsyncWrite for FailingFlushAsyncWrite {
        fn poll_write(
            self: std::pin::Pin<&mut Self>,
            _cx: &mut std::task::Context<'_>,
            buf: &[u8],
        ) -> std::task::Poll<std::io::Result<usize>> {
            std::task::Poll::Ready(Ok(buf.len()))
        }
        
        fn poll_flush(
            self: std::pin::Pin<&mut Self>,
            _cx: &mut std::task::Context<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            std::task::Poll::Ready(Err(std::io::Error::new(std::io::ErrorKind::Other, "Flush error")))
        }
        
        fn poll_shutdown(
            self: std::pin::Pin<&mut Self>,
            _cx: &mut std::task::Context<'_>,
        ) -> std::task::Poll<std::io::Result<()>> {
            std::task::Poll::Ready(Ok(()))
        }
    }

    #[tokio::test]
    async fn test_send_message_to_container_with_flush_error() {
        // Create a failing stdin
        let mut stdin = Box::new(FailingFlushAsyncWrite) as Box<dyn tokio::io::AsyncWrite + Unpin + Send>;
        
        // Create a JSON-RPC message
        let json_rpc_message = JsonRpcMessage::new_request(
            "test-method",
            Some(serde_json::json!({"key": "value"})),
            serde_json::Value::String("123".to_string())
        );
        
        // Send the message
        let result = StdioTransport::send_message_to_container(&mut stdin, &json_rpc_message).await;
        
        // Should fail with a transport error
        assert!(result.is_err());
        if let Err(Error::Transport(msg)) = result {
            assert!(msg.contains("Failed to flush container stdin"));
        } else {
            panic!("Expected Transport error");
        }
    }

}