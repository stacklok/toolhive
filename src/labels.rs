//! Container label constants and utilities
//!
//! This module provides constants and utility functions for working with container labels.

use std::collections::HashMap;

/// Label indicating that a container is managed by vibetool
pub const VIBETOOL_LABEL: &str = "vibetool";

/// Label value for containers managed by vibetool
pub const VIBETOOL_VALUE: &str = "true";

/// Label for container name
pub const NAME_LABEL: &str = "vibetool-name";

/// Label for transport mode
pub const TRANSPORT_LABEL: &str = "vibetool-transport";

/// Label for proxy port
pub const PORT_LABEL: &str = "vibetool-port";

/// Add standard vibetool labels to a labels HashMap
pub fn add_standard_labels(
    labels: &mut HashMap<String, String>,
    container_name: &str,
    transport: &str,
    port: u16,
) {
    labels.insert(VIBETOOL_LABEL.to_string(), VIBETOOL_VALUE.to_string());
    labels.insert(NAME_LABEL.to_string(), container_name.to_string());
    labels.insert(TRANSPORT_LABEL.to_string(), transport.to_string());
    labels.insert(PORT_LABEL.to_string(), port.to_string());
}

/// Get the transport mode from container labels
pub fn get_transport(labels: &HashMap<String, String>) -> &str {
    labels.get(TRANSPORT_LABEL).map_or("unknown", |s| s.as_str())
}

/// Get the proxy port from container labels
pub fn get_port(labels: &HashMap<String, String>) -> u16 {
    labels.get(PORT_LABEL)
        .and_then(|s| s.parse::<u16>().ok())
        .unwrap_or(0) // Default to 0 if not found or invalid
}

/// Check if a container is managed by vibetool
pub fn is_vibetool_container(labels: &HashMap<String, String>) -> bool {
    labels
        .get(VIBETOOL_LABEL)
        .map_or(false, |value| value == VIBETOOL_VALUE)
}

/// Format a label key-value pair for filtering
pub fn format_label_filter(key: &str, value: &str) -> String {
    format!("{}={}", key, value)
}

/// Format the standard vibetool label for filtering
pub fn format_vibetool_filter() -> String {
    format_label_filter(VIBETOOL_LABEL, VIBETOOL_VALUE)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_add_standard_labels() {
        let mut labels = HashMap::new();
        add_standard_labels(&mut labels, "test-container", "stdio", 8080);

        assert_eq!(labels.get(VIBETOOL_LABEL), Some(&VIBETOOL_VALUE.to_string()));
        assert_eq!(labels.get(NAME_LABEL), Some(&"test-container".to_string()));
        assert_eq!(labels.get(TRANSPORT_LABEL), Some(&"stdio".to_string()));
        assert_eq!(labels.get(PORT_LABEL), Some(&"8080".to_string()));
    }

    #[test]
    fn test_get_transport() {
        let mut labels = HashMap::new();
        labels.insert(TRANSPORT_LABEL.to_string(), "sse".to_string());

        assert_eq!(get_transport(&labels), "sse");

        // Test with missing transport label
        let empty_labels = HashMap::new();
        assert_eq!(get_transport(&empty_labels), "unknown");
    }

    #[test]
    fn test_get_port() {
        let mut labels = HashMap::new();
        labels.insert(PORT_LABEL.to_string(), "8080".to_string());

        assert_eq!(get_port(&labels), 8080);

        // Test with missing port label
        let empty_labels = HashMap::new();
        assert_eq!(get_port(&empty_labels), 0);

        // Test with invalid port value
        let mut invalid_labels = HashMap::new();
        invalid_labels.insert(PORT_LABEL.to_string(), "invalid".to_string());
        assert_eq!(get_port(&invalid_labels), 0);
    }

    #[test]
    fn test_is_vibetool_container() {
        let mut labels = HashMap::new();
        labels.insert(VIBETOOL_LABEL.to_string(), VIBETOOL_VALUE.to_string());

        assert!(is_vibetool_container(&labels));

        // Test with wrong value
        let mut wrong_labels = HashMap::new();
        wrong_labels.insert(VIBETOOL_LABEL.to_string(), "false".to_string());
        assert!(!is_vibetool_container(&wrong_labels));

        // Test with missing label
        let empty_labels = HashMap::new();
        assert!(!is_vibetool_container(&empty_labels));
    }

    #[test]
    fn test_format_label_filter() {
        assert_eq!(format_label_filter("key", "value"), "key=value");
        assert_eq!(format_label_filter("test", "123"), "test=123");
    }

    #[test]
    fn test_format_vibetool_filter() {
        assert_eq!(format_vibetool_filter(), format!("{}={}", VIBETOOL_LABEL, VIBETOOL_VALUE));
    }
}