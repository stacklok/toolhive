use async_trait::async_trait;
use hyper::{Body, Request, Response, Server, StatusCode};
use hyper::service::{make_service_fn, service_fn};
use std::any::Any;
use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex};
use tokio::sync::oneshot;

use crate::error::{Error, Result};
use crate::transport::{Transport, TransportMode};

/// SSE transport handler
pub struct SseTransport {
    port: u16,
    container_port: Arc<Mutex<u16>>,
    container_id: Arc<Mutex<Option<String>>>,
    container_name: Arc<Mutex<Option<String>>>,
    shutdown_tx: Arc<Mutex<Option<oneshot::Sender<()>>>>,
}

impl SseTransport {
    /// Create a new SSE transport handler
    pub fn new(port: u16) -> Self {
        Self {
            port,
            container_port: Arc::new(Mutex::new(8080)), // Default container port
            container_id: Arc::new(Mutex::new(None)),
            container_name: Arc::new(Mutex::new(None)),
            shutdown_tx: Arc::new(Mutex::new(None)),
        }
    }

    /// Create a new SSE transport handler with a custom container port
    pub fn with_container_port(port: u16, container_port: u16) -> Self {
        Self {
            port,
            container_port: Arc::new(Mutex::new(container_port)),
            container_id: Arc::new(Mutex::new(None)),
            container_name: Arc::new(Mutex::new(None)),
            shutdown_tx: Arc::new(Mutex::new(None)),
        }
    }

    /// Handle HTTP requests and proxy them to the container
    async fn handle_request(
        req: Request<Body>,
        container_port: u16,
        container_id: String,
    ) -> Result<Response<Body>> {
        // Create a new client for forwarding requests
        let client = hyper::Client::new();
        
        // Build the target URL
        let target_url = format!("http://localhost:{}", container_port);
        
        // Get the path and query from the original request
        let uri = req.uri();
        let path_and_query = uri.path_and_query()
            .map(|pq| pq.as_str())
            .unwrap_or("/");
        
        // Build the new URI
        let target_uri = format!("{}{}", target_url, path_and_query)
            .parse::<hyper::Uri>()
            .map_err(|e| Error::Transport(format!("Failed to parse target URI: {}", e)))?;
        
        // Create a new request with the same method, headers, and body
        let mut proxy_req = Request::builder()
            .method(req.method().clone())
            .uri(target_uri);
        
        // Copy headers
        let headers = proxy_req.headers_mut().unwrap();
        for (name, value) in req.headers() {
            if name != hyper::header::HOST {
                headers.insert(name.clone(), value.clone());
            }
        }
        
        // Add forwarding headers
        headers.insert("X-Forwarded-Host", req.uri().host().unwrap_or("localhost").parse().unwrap());
        headers.insert("X-Forwarded-Proto", "http".parse().unwrap());
        
        // Log the request
        println!("Proxying request to container {}: {} {}", 
            container_id, 
            req.method(), 
            req.uri().path()
        );
        
        // Forward the request
        let proxy_req = proxy_req.body(req.into_body())
            .map_err(|e| Error::Transport(format!("Failed to create proxy request: {}", e)))?;
        
        // Send the request and get the response
        let res = client.request(proxy_req).await
            .map_err(|e| Error::Transport(format!("Failed to forward request: {}", e)))?;
        
        // Return the response
        Ok(res)
    }
}

#[async_trait]
impl Transport for SseTransport {
    fn mode(&self) -> TransportMode {
        TransportMode::SSE
    }

    async fn setup(
        &self,
        container_id: &str,
        container_name: &str,
        port: Option<u16>,
        env_vars: &mut HashMap<String, String>,
    ) -> Result<()> {
        // Store container ID and name
        *self.container_id.lock().unwrap() = Some(container_id.to_string());
        *self.container_name.lock().unwrap() = Some(container_name.to_string());

        // Set container port if provided
        if let Some(p) = port {
            *self.container_port.lock().unwrap() = p;
        }
        
        // Get the current container port
        let container_port = *self.container_port.lock().unwrap();

        // Set environment variables for the container
        env_vars.insert("MCP_TRANSPORT".to_string(), "sse".to_string());
        env_vars.insert("MCP_PORT".to_string(), container_port.to_string());

        Ok(())
    }

    async fn start(&self) -> Result<()> {
        // Get container ID and name
        let container_id = match self.container_id.lock().unwrap().clone() {
            Some(id) => id,
            None => return Err(Error::Transport("Container ID not set".to_string())),
        };

        let container_name = match self.container_name.lock().unwrap().clone() {
            Some(name) => name,
            None => return Err(Error::Transport("Container name not set".to_string())),
        };

        // Get the container port
        let container_port = *self.container_port.lock().unwrap();
        
        // Create service function for handling requests
        let container_id_clone = container_id.clone();
        
        let make_svc = make_service_fn(move |_| {
            let container_id = container_id_clone.clone();
            
            async move {
                Ok::<_, hyper::Error>(service_fn(move |req: Request<Body>| {
                    let container_id = container_id.clone();
                    
                    async move {
                        match Self::handle_request(req, container_port, container_id).await {
                            Ok(response) => Ok::<_, hyper::Error>(response),
                            Err(e) => {
                                eprintln!("Error handling request: {}", e);
                                let response = Response::builder()
                                    .status(StatusCode::INTERNAL_SERVER_ERROR)
                                    .body(Body::from(format!("Error: {}", e)))
                                    .unwrap();
                                Ok(response)
                            }
                        }
                    }
                }))
            }
        });

        // Create the server
        let addr = SocketAddr::from(([0, 0, 0, 0], self.port));
        let server = Server::bind(&addr).serve(make_svc);
        
        println!("Reverse proxy started for container {} on port {}", container_name, self.port);
        println!("Forwarding to container port {}", container_port);

        // Create shutdown channel
        let (tx, rx) = oneshot::channel::<()>();
        *self.shutdown_tx.lock().unwrap() = Some(tx);

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

    async fn stop(&self) -> Result<()> {
        // Send shutdown signal if available
        if let Some(tx) = self.shutdown_tx.lock().unwrap().take() {
            let _ = tx.send(());
            println!("Reverse proxy stopped");
        }

        Ok(())
    }

    async fn is_running(&self) -> Result<bool> {
        // Check if shutdown channel is still available
        Ok(self.shutdown_tx.lock().unwrap().is_some())
    }
    
    fn as_any(&self) -> &dyn Any {
        self
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[tokio::test]
    async fn test_sse_transport_setup() {
        let transport = SseTransport::new(8080);
        let mut env_vars = HashMap::new();
        
        transport.setup("test-id", "test-container", Some(9000), &mut env_vars).await.unwrap();
        
        assert_eq!(env_vars.get("MCP_TRANSPORT").unwrap(), "sse");
        assert_eq!(env_vars.get("MCP_PORT").unwrap(), "9000");
        assert_eq!(*transport.container_port.lock().unwrap(), 9000);
    }

    #[tokio::test]
    async fn test_sse_transport_start_without_setup() {
        let transport = SseTransport::new(8080);
        let result = transport.start().await;
        
        assert!(result.is_err());
    }

    // Note: Testing the actual proxy functionality would require more complex setup
    // with a mock HTTP server, which is beyond the scope of this implementation
    
    #[tokio::test]
    async fn test_sse_transport_lifecycle() -> Result<()> {
        // Create a transport
        let transport = SseTransport::new(8082);
        let mut env_vars = HashMap::new();
        
        // Set up the transport
        transport.setup("test-id", "test-container", Some(9002), &mut env_vars).await?;
        
        // Start the transport
        transport.start().await?;
        
        // Check if it's running
        assert!(transport.is_running().await?);
        
        // Stop the transport
        transport.stop().await?;
        
        // Check if it's stopped
        assert!(!transport.is_running().await?);
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_sse_transport_with_container_port() -> Result<()> {
        // Create a transport with a custom container port
        let transport = SseTransport::with_container_port(8083, 9003);
        let mut env_vars = HashMap::new();
        
        // Set up the transport
        transport.setup("test-id", "test-container", None, &mut env_vars).await?;
        
        // Check that the container port was set correctly
        assert_eq!(*transport.container_port.lock().unwrap(), 9003);
        
        // Check that the environment variables were set correctly
        assert_eq!(env_vars.get("MCP_TRANSPORT").unwrap(), "sse");
        assert_eq!(env_vars.get("MCP_PORT").unwrap(), "9003");
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_sse_transport_setup_with_port_override() -> Result<()> {
        // Create a transport
        let transport = SseTransport::new(8084);
        let mut env_vars = HashMap::new();
        
        // Set up the transport with a port override
        transport.setup("test-id", "test-container", Some(9004), &mut env_vars).await?;
        
        // Check that the container port was set correctly
        assert_eq!(*transport.container_port.lock().unwrap(), 9004);
        
        // Check that the environment variables were set correctly
        assert_eq!(env_vars.get("MCP_TRANSPORT").unwrap(), "sse");
        assert_eq!(env_vars.get("MCP_PORT").unwrap(), "9004");
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_sse_transport_stop_when_not_running() -> Result<()> {
        // Create a transport
        let transport = SseTransport::new(8085);
        
        // Stop the transport (should not fail even though it's not running)
        transport.stop().await?;
        
        // Check if it's running (should be false)
        assert!(!transport.is_running().await?);
        
        Ok(())
    }
    
    #[tokio::test]
    async fn test_sse_transport_is_running_with_shutdown_tx() -> Result<()> {
        // Create a transport
        let transport = SseTransport::new(8086);
        
        // Manually set the shutdown_tx to simulate a running transport
        let (tx, _rx) = tokio::sync::oneshot::channel::<()>();
        *transport.shutdown_tx.lock().unwrap() = Some(tx);
        
        // Check if it's running (should be true)
        assert!(transport.is_running().await?);
        
        // Stop the transport
        transport.stop().await?;
        
        // Check if it's running (should be false)
        assert!(!transport.is_running().await?);
        
        Ok(())
    }
}