use serde::{Deserialize, Serialize};
use std::fmt;

/// SSE message type for Server-Sent Events
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SSEMessage {
    /// The event type (e.g., "message", "endpoint", "initialize")
    pub event_type: String,
    /// The data payload for the event
    pub data: String,
    /// Optional ID for the event
    #[serde(skip_serializing_if = "Option::is_none")]
    pub id: Option<String>,
    /// Optional target client ID (None means broadcast to all clients)
    #[serde(skip_serializing_if = "Option::is_none")]
    pub target_client_id: Option<uuid::Uuid>,
}

impl SSEMessage {
    /// Create a new SSE message
    pub fn new(event_type: &str, data: &str) -> Self {
        Self {
            event_type: event_type.to_string(),
            data: data.to_string(),
            id: None,
            target_client_id: None,
        }
    }

    /// Create a new SSE message with an ID
    pub fn with_id(event_type: &str, data: &str, id: &str) -> Self {
        Self {
            event_type: event_type.to_string(),
            data: data.to_string(),
            id: Some(id.to_string()),
            target_client_id: None,
        }
    }

    /// Create a new SSE message with a target client ID
    pub fn with_target(event_type: &str, data: &str, target_client_id: uuid::Uuid) -> Self {
        Self {
            event_type: event_type.to_string(),
            data: data.to_string(),
            id: None,
            target_client_id: Some(target_client_id),
        }
    }

    /// Set the target client ID
    pub fn set_target(&mut self, target_client_id: uuid::Uuid) -> &mut Self {
        self.target_client_id = Some(target_client_id);
        self
    }

    /// Set the ID
    pub fn set_id(&mut self, id: &str) -> &mut Self {
        self.id = Some(id.to_string());
        self
    }

    /// Serialize the SSE message to a string in the SSE format
    pub fn to_sse_string(&self) -> String {
        let mut result = format!("event: {}\ndata: {}", self.event_type, self.data);

        if let Some(id) = &self.id {
            result.push_str(&format!("\nid: {}", id));
        }

        // End with double newline as per SSE spec
        result.push_str("\n\n");

        result
    }
}

impl fmt::Display for SSEMessage {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}", self.to_sse_string())
    }
}

/// Message type for pending SSE messages
#[derive(Debug, Clone)]
pub struct PendingSSEMessage {
    /// The SSE message
    pub message: SSEMessage,
}

impl From<SSEMessage> for PendingSSEMessage {
    fn from(message: SSEMessage) -> Self {
        Self { message }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_sse_message_creation() {
        // Test basic message
        let message = SSEMessage::new("test-event", "test-data");
        assert_eq!(message.event_type, "test-event");
        assert_eq!(message.data, "test-data");
        assert_eq!(message.id, None);
        assert_eq!(message.target_client_id, None);

        // Test message with ID
        let message = SSEMessage::with_id("test-event", "test-data", "123");
        assert_eq!(message.event_type, "test-event");
        assert_eq!(message.data, "test-data");
        assert_eq!(message.id, Some("123".to_string()));
        assert_eq!(message.target_client_id, None);

        // Test message with target
        let target_id = uuid::Uuid::new_v4();
        let message = SSEMessage::with_target("test-event", "test-data", target_id);
        assert_eq!(message.event_type, "test-event");
        assert_eq!(message.data, "test-data");
        assert_eq!(message.id, None);
        assert_eq!(message.target_client_id, Some(target_id));
    }

    #[test]
    fn test_sse_message_setters() {
        // Test setters
        let mut message = SSEMessage::new("test-event", "test-data");
        let target_id = uuid::Uuid::new_v4();

        message.set_id("123").set_target(target_id);

        assert_eq!(message.id, Some("123".to_string()));
        assert_eq!(message.target_client_id, Some(target_id));
    }

    #[test]
    fn test_sse_message_to_sse_string() {
        // Test basic message
        let message = SSEMessage::new("test-event", "test-data");
        let expected = "event: test-event\ndata: test-data\n\n";
        assert_eq!(message.to_sse_string(), expected);

        // Test message with ID
        let message = SSEMessage::with_id("test-event", "test-data", "123");
        let expected = "event: test-event\ndata: test-data\nid: 123\n\n";
        assert_eq!(message.to_sse_string(), expected);
    }

    #[test]
    fn test_sse_message_display() {
        let message = SSEMessage::new("test-event", "test-data");
        let expected = "event: test-event\ndata: test-data\n\n";
        assert_eq!(format!("{}", message), expected);
    }

    #[test]
    fn test_pending_sse_message() {
        let message = SSEMessage::new("test-event", "test-data");
        let pending = PendingSSEMessage::from(message.clone());

        assert_eq!(pending.message.event_type, message.event_type);
        assert_eq!(pending.message.data, message.data);
    }
}
