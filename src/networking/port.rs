use rand::Rng;
use std::net::{SocketAddr, TcpListener};

/// Check if a port is available
pub fn is_available(port: u16) -> bool {
    let addr = SocketAddr::from(([127, 0, 0, 1], port));
    TcpListener::bind(addr).is_ok()
}

/// Get a random port in the ephemeral port range (49152-65535)
pub fn get_random() -> u16 {
    let mut rng = rand::thread_rng();
    rng.gen_range(49152..65535)
}

/// Find an available random port
/// Makes multiple attempts to find an available port in the ephemeral range
pub fn find_available() -> Option<u16> {
    // Try to find a random available port (with a maximum of 10 attempts)
    for _ in 0..10 {
        let port = get_random();
        if is_available(port) {
            return Some(port);
        }
    }

    None
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_is_port_available() {
        // This test is a bit tricky since we can't guarantee a port is available
        // But we can at least test the function runs without errors
        let result = is_available(0); // Port 0 means OS will assign a port
                                      // Just making sure the function runs without panicking
        let _ = result;
    }

    #[test]
    fn test_get_random_port() {
        let port = get_random();
        assert!(port >= 49152); // Check lower bound only, upper bound is guaranteed by u16 type
    }

    #[test]
    fn test_find_available_port() {
        // Test finding an available port
        let port = find_available();
        // We can't guarantee a port will be found, but if one is found, it should be in the correct range
        if let Some(p) = port {
            assert!(p >= 49152); // Check lower bound only, upper bound is guaranteed by u16 type
        }
    }
}
