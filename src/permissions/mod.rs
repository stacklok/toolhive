//! Permissions module for mcp-lok
//!
//! This module handles permission profiles for MCP servers.

use anyhow::{Context, Result};
use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::fs;
use std::path::Path;

/// Network permissions configuration
#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct NetworkPermissions {
    /// Outbound network permissions
    pub outbound: OutboundNetworkPermissions,
}

/// Outbound network permissions configuration
#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct OutboundNetworkPermissions {
    /// Allow all outbound connections (not recommended for production)
    #[serde(default)]
    pub insecure_allow_all: bool,
    
    /// Allowed transport protocols (tcp, udp)
    #[serde(default)]
    pub allow_transport: HashSet<String>,
    
    /// Allowed host destinations
    #[serde(default)]
    pub allow_host: HashSet<String>,
    
    /// Allowed destination ports
    #[serde(default)]
    pub allow_port: HashSet<u16>,
}

/// Permission profile for an MCP server
#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct PermissionProfile {
    /// Paths with read permission
    #[serde(default)]
    pub read: HashSet<String>,
    
    /// Paths with write permission
    #[serde(default)]
    pub write: HashSet<String>,
    
    /// Network permissions
    pub network: Option<NetworkPermissions>,
}

impl PermissionProfile {
    /// Create a new empty permission profile
    pub fn new() -> Self {
        PermissionProfile {
            read: HashSet::new(),
            write: HashSet::new(),
            network: None,
        }
    }

    /// Load a permission profile from a file
    pub fn from_file<P: AsRef<Path>>(path: P) -> Result<Self> {
        let content = fs::read_to_string(path.as_ref())
            .with_context(|| format!("Failed to read permission profile from {:?}", path.as_ref()))?;
        
        let profile: PermissionProfile = serde_json::from_str(&content)
            .with_context(|| "Failed to parse permission profile JSON")?;
        
        Ok(profile)
    }

    /// Get the stdio profile (minimal permissions for stdio transport)
    pub fn stdio_profile() -> Self {
        // Create an empty profile with no permissions
        PermissionProfile::new()
    }

    /// Get the network profile (allows outbound network connections)
    pub fn network_profile() -> Self {
        let mut profile = PermissionProfile::new();
        
        // Configure network permissions
        let outbound = OutboundNetworkPermissions {
            insecure_allow_all: true,
            allow_transport: HashSet::new(),
            allow_host: HashSet::new(),
            allow_port: HashSet::new(),
        };
        
        profile.network = Some(NetworkPermissions { outbound });
        profile
    }
}