use vibetool::permissions::profile::{NetworkPermissions, OutboundNetworkPermissions, PermissionProfile};

#[test]
fn test_builtin_stdio_profile() {
    // Get the stdio profile
    let profile = PermissionProfile::builtin_stdio_profile();

    // Check that it has the expected values
    assert!(profile.read.is_empty());
    assert!(profile.write.is_empty());
    assert!(profile.network.is_none());
}

#[test]
fn test_builtin_network_profile() {
    // Get the network profile
    let profile = PermissionProfile::builtin_network_profile();

    // Check that it has the expected values
    assert!(profile.read.is_empty());
    assert!(profile.write.is_empty());
    
    // Check network permissions
    let network = profile.network.unwrap();
    assert!(network.outbound.is_some());
    let outbound = network.outbound.unwrap();
    assert!(outbound.insecure_allow_all);
    // The builtin network profile has empty allow_transport
    assert!(outbound.allow_transport.is_empty());
}

#[test]
fn test_to_container_config() {
    // Create a profile with network permissions
    let profile = PermissionProfile {
        read: vec!["/etc/hosts".to_string()],
        write: vec!["/tmp".to_string()],
        network: Some(NetworkPermissions {
            outbound: Some(OutboundNetworkPermissions {
                insecure_allow_all: true, // This needs to be true for bridge mode
                allow_transport: vec![], // Must be empty when insecure_allow_all is true
                allow_host: vec![],      // Must be empty when insecure_allow_all is true
                allow_port: vec![],      // Must be empty when insecure_allow_all is true
            }),
        }),
    };

    // Convert to container config
    let config = profile.to_container_config().unwrap();

    // Check that it has the expected values
    assert_eq!(config.mounts.len(), 2);
    
    // Find the read-only mount
    let hosts_mount = config.mounts.iter().find(|m| m.source == "/etc/hosts").unwrap();
    assert_eq!(hosts_mount.target, "/etc/hosts");
    assert_eq!(hosts_mount.read_only, true);
    
    // Find the read-write mount
    let tmp_mount = config.mounts.iter().find(|m| m.source == "/tmp").unwrap();
    assert_eq!(tmp_mount.target, "/tmp");
    assert_eq!(tmp_mount.read_only, false);

    // Check network config
    assert_eq!(config.network_mode, "bridge");
    assert_eq!(config.cap_drop, vec!["ALL"]);
    assert_eq!(config.cap_add, vec!["NET_BIND_SERVICE"]);
    assert_eq!(config.security_opt, vec!["no-new-privileges"]);
}

#[test]
fn test_to_container_config_no_network() {
    // Create a profile without network permissions
    let profile = PermissionProfile {
        read: vec!["/etc/hosts".to_string()],
        write: vec!["/tmp".to_string()],
        network: None,
    };

    // Convert to container config
    let config = profile.to_container_config().unwrap();

    // Check that it has the expected values
    assert_eq!(config.mounts.len(), 2);
    
    // Find the read-only mount
    let hosts_mount = config.mounts.iter().find(|m| m.source == "/etc/hosts").unwrap();
    assert_eq!(hosts_mount.target, "/etc/hosts");
    assert_eq!(hosts_mount.read_only, true);
    
    // Find the read-write mount
    let tmp_mount = config.mounts.iter().find(|m| m.source == "/tmp").unwrap();
    assert_eq!(tmp_mount.target, "/tmp");
    assert_eq!(tmp_mount.read_only, false);

    // Check network config
    assert_eq!(config.network_mode, "none");
    assert_eq!(config.cap_drop, vec!["ALL"]);
    assert_eq!(config.cap_add, vec!["NET_BIND_SERVICE"]);
    assert_eq!(config.security_opt, vec!["no-new-privileges"]);
}

#[test]
fn test_to_container_config_read_only() {
    // Create a profile with read-only permissions
    let profile = PermissionProfile {
        read: vec!["/etc/hosts".to_string()],
        write: vec![],
        network: Some(NetworkPermissions {
            outbound: Some(OutboundNetworkPermissions {
                insecure_allow_all: true, // This needs to be true for bridge mode
                allow_transport: vec![], // Must be empty when insecure_allow_all is true
                allow_host: vec![],      // Must be empty when insecure_allow_all is true
                allow_port: vec![],      // Must be empty when insecure_allow_all is true
            }),
        }),
    };

    // Convert to container config
    let config = profile.to_container_config().unwrap();

    // Check that it has the expected values
    assert_eq!(config.mounts.len(), 1);
    assert_eq!(config.mounts[0].source, "/etc/hosts");
    assert_eq!(config.mounts[0].target, "/etc/hosts");
    assert_eq!(config.mounts[0].read_only, true);

    // Check network config
    assert_eq!(config.network_mode, "bridge");
    assert_eq!(config.cap_drop, vec!["ALL"]);
    assert_eq!(config.cap_add, vec!["NET_BIND_SERVICE"]);
    assert_eq!(config.security_opt, vec!["no-new-privileges"]);
}

#[test]
fn test_to_container_config_multiple_mounts() {
    // Create a profile with multiple mounts
    let profile = PermissionProfile {
        read: vec![
            "/etc/hosts".to_string(),
            "/etc/resolv.conf".to_string(),
        ],
        write: vec![
            "/tmp".to_string(),
            "/var/log".to_string(),
        ],
        network: Some(NetworkPermissions {
            outbound: Some(OutboundNetworkPermissions {
                insecure_allow_all: true, // This needs to be true for bridge mode
                allow_transport: vec![], // Must be empty when insecure_allow_all is true
                allow_host: vec![],      // Must be empty when insecure_allow_all is true
                allow_port: vec![],      // Must be empty when insecure_allow_all is true
            }),
        }),
    };

    // Convert to container config
    let config = profile.to_container_config().unwrap();

    // Check that it has the expected values
    assert_eq!(config.mounts.len(), 4);
    
    // Check /etc/hosts mount (read-only)
    let hosts_mount = config.mounts.iter().find(|m| m.source == "/etc/hosts").unwrap();
    assert_eq!(hosts_mount.target, "/etc/hosts");
    assert_eq!(hosts_mount.read_only, true);
    
    // Check /etc/resolv.conf mount (read-only)
    let resolv_mount = config.mounts.iter().find(|m| m.source == "/etc/resolv.conf").unwrap();
    assert_eq!(resolv_mount.target, "/etc/resolv.conf");
    assert_eq!(resolv_mount.read_only, true);
    
    // Check /tmp mount (read-write)
    let tmp_mount = config.mounts.iter().find(|m| m.source == "/tmp").unwrap();
    assert_eq!(tmp_mount.target, "/tmp");
    assert_eq!(tmp_mount.read_only, false);
    
    // Check /var/log mount (read-write)
    let log_mount = config.mounts.iter().find(|m| m.source == "/var/log").unwrap();
    assert_eq!(log_mount.target, "/var/log");
    assert_eq!(log_mount.read_only, false);

    // Check network config
    assert_eq!(config.network_mode, "bridge");
    assert_eq!(config.cap_drop, vec!["ALL"]);
    assert_eq!(config.cap_add, vec!["NET_BIND_SERVICE"]);
    assert_eq!(config.security_opt, vec!["no-new-privileges"]);
}

#[test]
fn test_restricted_network() {
    // This test demonstrates how to create a profile with restricted network access
    // Note: This is not currently supported by the implementation, which only allows
    // either full network access (insecure_allow_all=true) or no network access (network_mode=none)
    let profile = PermissionProfile {
        read: vec!["/etc/hosts".to_string()],
        write: vec!["/tmp".to_string()],
        network: Some(NetworkPermissions {
            outbound: Some(OutboundNetworkPermissions {
                insecure_allow_all: false,
                allow_transport: vec!["tcp".to_string()],
                allow_host: vec!["localhost".to_string()],
                allow_port: vec![80, 443],
            }),
        }),
    };

    // Validate the profile
    assert!(profile.validate().is_ok());

    // Convert to container config
    let config = profile.to_container_config().unwrap();

    // With the current implementation, network_mode will be "none" because insecure_allow_all is false
    assert_eq!(config.network_mode, "none");
}