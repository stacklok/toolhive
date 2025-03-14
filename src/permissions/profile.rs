use serde::{Deserialize, Serialize};
use std::fs;
use std::path::{Path};

use crate::error::{Error, Result};

/// Permission profile for MCP servers
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PermissionProfile {
    /// Paths that can be read
    #[serde(default)]
    pub read: Vec<String>,
    
    /// Paths that can be written
    #[serde(default)]
    pub write: Vec<String>,
    
    /// Network permissions
    #[serde(default)]
    pub network: Option<NetworkPermissions>,
}

/// Network permissions
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct NetworkPermissions {
    /// Outbound network permissions
    #[serde(default)]
    pub outbound: Option<OutboundNetworkPermissions>,
}

/// Outbound network permissions
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct OutboundNetworkPermissions {
    /// Allow all outbound connections
    #[serde(default)]
    pub insecure_allow_all: bool,
    
    /// Allowed transport protocols
    #[serde(default)]
    pub allow_transport: Vec<String>,
    
    /// Allowed hosts
    #[serde(default)]
    pub allow_host: Vec<String>,
    
    /// Allowed ports
    #[serde(default)]
    pub allow_port: Vec<u16>,
}

/// Container permission configuration
#[derive(Debug, Clone)]
pub struct ContainerPermissionConfig {
    /// Mounts for the container
    pub mounts: Vec<ContainerMount>,
    
    /// Network mode for the container
    pub network_mode: String,
    
    /// Capabilities to drop
    pub cap_drop: Vec<String>,
    
    /// Capabilities to add
    pub cap_add: Vec<String>,
    
    /// Security options
    pub security_opt: Vec<String>,
}

/// Container mount
#[derive(Debug, Clone)]
pub struct ContainerMount {
    /// Source path on the host
    pub source: String,
    
    /// Target path in the container
    pub target: String,
    
    /// Whether the mount is read-only
    pub read_only: bool,
}

impl PermissionProfile {
    /// Load a permission profile from a file
    pub fn from_file<P: AsRef<Path>>(path: P) -> Result<Self> {
        let content = fs::read_to_string(path.as_ref())
            .map_err(|e| Error::Io(e))?;
        
        let profile: Self = serde_json::from_str(&content)
            .map_err(|e| Error::Json(e))?;
        
        profile.validate()?;
        
        Ok(profile)
    }
    
    /// Load a built-in permission profile
    pub fn builtin(name: &str) -> Result<Self> {
        match name {
            "stdio" => Ok(Self::builtin_stdio_profile()),
            "network" => Ok(Self::builtin_network_profile()),
            _ => Err(Error::InvalidArgument(format!("Unknown built-in profile: {}", name))),
        }
    }
    
    /// Get the built-in stdio permission profile
    pub fn builtin_stdio_profile() -> Self {
        Self {
            read: vec!["/var/run/mcp.sock".to_string()],
            write: vec!["/var/run/mcp.sock".to_string()],
            network: None,
        }
    }
    
    /// Get the built-in network permission profile
    pub fn builtin_network_profile() -> Self {
        Self {
            read: vec!["/var/run/mcp.sock".to_string()],
            write: vec!["/var/run/mcp.sock".to_string()],
            network: Some(NetworkPermissions {
                outbound: Some(OutboundNetworkPermissions {
                    insecure_allow_all: true,
                    allow_transport: vec![],
                    allow_host: vec![],
                    allow_port: vec![],
                }),
            }),
        }
    }
    
    /// Validate the permission profile
    pub fn validate(&self) -> Result<()> {
        // Validate paths
        for path in &self.read {
            if !path.starts_with('/') {
                return Err(Error::InvalidArgument(format!("Invalid path: {}", path)));
            }
        }
        
        for path in &self.write {
            if !path.starts_with('/') {
                return Err(Error::InvalidArgument(format!("Invalid path: {}", path)));
            }
        }
        
        // Validate network permissions
        if let Some(network) = &self.network {
            if let Some(outbound) = &network.outbound {
                if outbound.insecure_allow_all {
                    if !outbound.allow_transport.is_empty() || !outbound.allow_host.is_empty() || !outbound.allow_port.is_empty() {
                        return Err(Error::InvalidArgument(
                            "Cannot specify allow_transport, allow_host, or allow_port when insecure_allow_all is true".to_string(),
                        ));
                    }
                }
            }
        }
        
        Ok(())
    }
    
    /// Convert the permission profile to a container configuration
    pub fn to_container_config(&self) -> Result<ContainerPermissionConfig> {
        // Validate the profile
        self.validate()?;
        
        // Create mounts for read and write paths
        let mut mounts = Vec::new();
        
        // Add read-only mounts
        for path in &self.read {
            if !self.write.contains(path) {
                mounts.push(ContainerMount {
                    source: path.clone(),
                    target: path.clone(),
                    read_only: true,
                });
            }
        }
        
        // Add read-write mounts
        for path in &self.write {
            mounts.push(ContainerMount {
                source: path.clone(),
                target: path.clone(),
                read_only: false,
            });
        }
        
        // Set network mode
        let network_mode = match &self.network {
            Some(network) => {
                if let Some(outbound) = &network.outbound {
                    if outbound.insecure_allow_all {
                        "bridge".to_string()
                    } else {
                        "none".to_string()
                    }
                } else {
                    "none".to_string()
                }
            }
            None => "none".to_string(),
        };
        
        // Set capabilities to drop
        let cap_drop = vec![
            "ALL".to_string(),
        ];
        
        // Set capabilities to add
        let cap_add = vec![
            "NET_BIND_SERVICE".to_string(),
        ];
        
        // Set security options
        let security_opt = vec![
            "no-new-privileges".to_string(),
        ];
        
        Ok(ContainerPermissionConfig {
            mounts,
            network_mode,
            cap_drop,
            cap_add,
            security_opt,
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashSet;
    
    
    #[test]
    fn test_default_permission_profile() {
        let profile = PermissionProfile {
            read: vec![],
            write: vec![],
            network: None,
        };
        
        assert!(profile.validate().is_ok());
    }
    
    #[test]
    fn test_builtin_stdio_profile() {
        let profile = PermissionProfile::builtin_stdio_profile();
        
        assert_eq!(profile.read, vec!["/var/run/mcp.sock"]);
        assert_eq!(profile.write, vec!["/var/run/mcp.sock"]);
        assert!(profile.network.is_none());
    }
    
    #[test]
    fn test_builtin_network_profile() {
        let profile = PermissionProfile::builtin_network_profile();
        
        assert_eq!(profile.read, vec!["/var/run/mcp.sock"]);
        assert_eq!(profile.write, vec!["/var/run/mcp.sock"]);
        assert!(profile.network.is_some());
        
        let network = profile.network.unwrap();
        assert!(network.outbound.is_some());
        
        let outbound = network.outbound.unwrap();
        assert!(outbound.insecure_allow_all);
    }
    
    #[test]
    fn test_validate_invalid_path() {
        let profile = PermissionProfile {
            read: vec!["invalid".to_string()],
            write: vec![],
            network: None,
        };
        
        assert!(profile.validate().is_err());
    }
    
    #[test]
    fn test_validate_inconsistent_network() {
        let profile = PermissionProfile {
            read: vec![],
            write: vec![],
            network: Some(NetworkPermissions {
                outbound: Some(OutboundNetworkPermissions {
                    insecure_allow_all: true,
                    allow_transport: vec!["tcp".to_string()],
                    allow_host: vec![],
                    allow_port: vec![],
                }),
            }),
        };
        
        assert!(profile.validate().is_err());
    }
    
    #[test]
    fn test_to_container_config() {
        let profile = PermissionProfile {
            read: vec!["/var/run/mcp.sock".to_string(), "/etc/hosts".to_string()],
            write: vec!["/var/run/mcp.sock".to_string()],
            network: Some(NetworkPermissions {
                outbound: Some(OutboundNetworkPermissions {
                    insecure_allow_all: true,
                    allow_transport: vec![],
                    allow_host: vec![],
                    allow_port: vec![],
                }),
            }),
        };
        
        let config = profile.to_container_config().unwrap();
        
        // Check mounts
        assert_eq!(config.mounts.len(), 2);
        
        let mount_paths: HashSet<String> = config.mounts.iter()
            .map(|m| m.source.clone())
            .collect();
        
        assert!(mount_paths.contains("/var/run/mcp.sock"));
        assert!(mount_paths.contains("/etc/hosts"));
        
        // Check network mode
        assert_eq!(config.network_mode, "bridge");
        
        // Check capabilities
        assert_eq!(config.cap_drop, vec!["ALL"]);
        assert_eq!(config.cap_add, vec!["NET_BIND_SERVICE"]);
        
        // Check security options
        assert_eq!(config.security_opt, vec!["no-new-privileges"]);
    }
    
    #[test]
    fn test_from_file() {
        // This test would require a file to read from
        // In a real implementation, we would create a temporary file
        // For now, we'll just test that the function exists
        assert!(PermissionProfile::from_file("nonexistent").is_err());
    }
}