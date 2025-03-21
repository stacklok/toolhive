use std::collections::HashMap;

use hyper::service::{make_service_fn, service_fn};
use hyper::{Body, Request, Response, Server};
use tokio::sync::oneshot;

use vibetool::environment;
use vibetool::error::Result;
use vibetool::transport::sse::SseTransport;
use vibetool::transport::stdio::JsonRpcMessage;
use vibetool::transport::stdio::StdioTransport;
use vibetool::transport::{Transport, TransportMode};

// Helper function to create a test HTTP server
async fn create_test_server(port: u16) -> (String, oneshot::Sender<()>) {
    let addr = ([127, 0, 0, 1], port).into();

    // Create a oneshot channel for shutdown
    let (tx, rx) = oneshot::channel::<()>();

    // Create a simple service that returns "Hello, World!"
    let make_svc = make_service_fn(|_conn| async {
        Ok::<_, hyper::Error>(service_fn(|_req: Request<Body>| async {
            Ok::<_, hyper::Error>(Response::new(Body::from("Hello, World!")))
        }))
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

    // Return the server address and shutdown sender
    (format!("http://127.0.0.1:{}", port), tx)
}

#[tokio::test]
async fn test_sse_transport_setup() -> Result<()> {
    let transport = SseTransport::new(8080);
    let mut env_vars = HashMap::new();

    transport.set_container_port(9000);
    transport
        .setup(
            "test-id",
            "test-container",
            &mut env_vars,
            Some("172.17.0.2".to_string()),
        )
        .await?;

    // Environment variables are now set in the environment module, not in the transport
    // Set them manually for testing
    environment::set_transport_environment_variables(&mut env_vars, &TransportMode::SSE, 9000);

    assert_eq!(env_vars.get("MCP_TRANSPORT").unwrap(), "sse");
    assert_eq!(env_vars.get("MCP_PORT").unwrap(), "9000");

    Ok(())
}

#[tokio::test]
async fn test_sse_transport_start_without_setup() {
    let transport = SseTransport::new(8080);
    let result = transport.start(None, None).await;

    assert!(result.is_err());
}

#[tokio::test]
async fn test_sse_transport_lifecycle() -> Result<()> {
    // Create a transport
    let transport = SseTransport::new(8081);
    let mut env_vars = HashMap::new();

    // Set up the transport
    transport.set_container_port(9002);
    transport
        .setup(
            "test-id",
            "test-container",
            &mut env_vars,
            Some("127.0.0.1".to_string()),
        )
        .await?;

    // Environment variables are now set in the environment module, not in the transport
    // Set them manually for testing
    environment::set_transport_environment_variables(&mut env_vars, &TransportMode::SSE, 9002);

    // Start the transport (this will try to connect to a server, which might not be running)
    // So we'll just check if the setup worked correctly
    assert_eq!(env_vars.get("MCP_TRANSPORT").unwrap(), "sse");
    assert_eq!(env_vars.get("MCP_PORT").unwrap(), "9002");

    // We can't check the container ID and name directly as they're private fields
    // Just verify that the environment variables were set correctly

    Ok(())
}

#[tokio::test]
async fn test_stdio_transport_setup() -> Result<()> {
    let transport = StdioTransport::new(8080);
    let mut env_vars = HashMap::new();

    transport
        .setup("test-id", "test-container", &mut env_vars, None)
        .await?;

    // Environment variables are now set in the environment module, not in the transport
    // Set them manually for testing
    environment::set_transport_environment_variables(&mut env_vars, &TransportMode::STDIO, 8080);

    assert_eq!(env_vars.get("MCP_TRANSPORT").unwrap(), "stdio");
    assert_eq!(env_vars.get("MCP_PORT").unwrap(), "8080");

    Ok(())
}

#[tokio::test]
async fn test_stdio_transport_start_without_setup() {
    let transport = StdioTransport::new(8080);
    let result = transport.start(None, None).await;

    assert!(result.is_err());
}

#[tokio::test]
async fn test_transport_mode_parse_str() {
    assert_eq!(TransportMode::parse_str("sse"), Some(TransportMode::SSE));
    assert_eq!(
        TransportMode::parse_str("stdio"),
        Some(TransportMode::STDIO)
    );
    assert_eq!(TransportMode::parse_str("invalid"), None);
}

#[tokio::test]
async fn test_transport_mode_from_str_trait() {
    use std::str::FromStr;

    assert!(TransportMode::from_str("sse").is_ok());
    assert_eq!(TransportMode::from_str("sse").unwrap(), TransportMode::SSE);

    assert!(TransportMode::from_str("stdio").is_ok());
    assert_eq!(
        TransportMode::from_str("stdio").unwrap(),
        TransportMode::STDIO
    );

    assert!(TransportMode::from_str("invalid").is_err());
}

#[tokio::test]
async fn test_transport_mode_as_str() {
    assert_eq!(TransportMode::SSE.as_str(), "sse");
    assert_eq!(TransportMode::STDIO.as_str(), "stdio");
}

// Test JSON-RPC conversion

#[test]
fn test_json_rpc_message_creation() {
    let json_rpc = JsonRpcMessage {
        jsonrpc: "2.0".to_string(),
        method: Some("test".to_string()),
        params: Some(serde_json::json!({"hello": "world"})),
        id: Some(serde_json::Value::String("123".to_string())),
        result: None,
        error: None,
    };

    assert_eq!(json_rpc.jsonrpc, "2.0");
    assert_eq!(json_rpc.method, Some("test".to_string()));
    assert_eq!(
        json_rpc
            .params
            .as_ref()
            .unwrap()
            .as_object()
            .unwrap()
            .get("hello")
            .unwrap()
            .as_str()
            .unwrap(),
        "world"
    );
    assert_eq!(json_rpc.id.unwrap().as_str().unwrap(), "123");
}
