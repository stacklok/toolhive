package config

import (
	"fmt"
	"strings"
)

// SupportedAuthFiles maps file type names to their target paths in the container
var SupportedAuthFiles = map[string]string{
	"npmrc":  "/root/.npmrc",
	"netrc":  "/root/.netrc",
	"yarnrc": "/root/.yarnrc",
}

// ValidateBuildAuthFileName checks if the file name is supported
func ValidateBuildAuthFileName(name string) error {
	if _, ok := SupportedAuthFiles[name]; !ok {
		supported := make([]string, 0, len(SupportedAuthFiles))
		for k := range SupportedAuthFiles {
			supported = append(supported, k)
		}
		return fmt.Errorf("unsupported auth file type %q; supported types: %s", name, strings.Join(supported, ", "))
	}
	return nil
}

// setBuildAuthFile stores the content for an auth file
func setBuildAuthFile(p Provider, name, content string) error {
	if err := ValidateBuildAuthFileName(name); err != nil {
		return err
	}

	return p.UpdateConfig(func(c *Config) {
		if c.BuildAuthFiles == nil {
			c.BuildAuthFiles = make(map[string]string)
		}
		c.BuildAuthFiles[name] = content
	})
}

// getBuildAuthFile retrieves the content for an auth file
func getBuildAuthFile(p Provider, name string) (content string, exists bool) {
	config := p.GetConfig()
	if config.BuildAuthFiles == nil {
		return "", false
	}
	content, exists = config.BuildAuthFiles[name]
	return content, exists
}

// getAllBuildAuthFiles returns all configured auth files
func getAllBuildAuthFiles(p Provider) map[string]string {
	config := p.GetConfig()
	if config.BuildAuthFiles == nil {
		return make(map[string]string)
	}
	result := make(map[string]string, len(config.BuildAuthFiles))
	for k, v := range config.BuildAuthFiles {
		result[k] = v
	}
	return result
}

// unsetBuildAuthFile removes an auth file configuration
func unsetBuildAuthFile(p Provider, name string) error {
	return p.UpdateConfig(func(c *Config) {
		if c.BuildAuthFiles != nil {
			delete(c.BuildAuthFiles, name)
		}
	})
}

// unsetAllBuildAuthFiles removes all auth file configurations
func unsetAllBuildAuthFiles(p Provider) error {
	return p.UpdateConfig(func(c *Config) {
		c.BuildAuthFiles = nil
	})
}
