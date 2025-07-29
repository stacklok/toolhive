package container

import (
	"fmt"
	"strings"
	"time"
)

// GetOrGenerateContainerName generates a container name if not provided.
// It returns both the container name and the base name.
// If containerName is not empty, it will be used as both the container name and base name.
// If containerName is empty, a name will be generated based on the image.
func GetOrGenerateContainerName(containerName, image string) (string, string) {
	var baseName string

	if containerName == "" {
		// Generate a container name from the image
		baseName = generateContainerBaseName(image)
		containerName = appendTimestamp(baseName)
	} else {
		// If container name is provided, use it as the base name
		baseName = containerName
	}

	return containerName, baseName
}

// generateContainerBaseName generates a base name for a container from the image name
func generateContainerBaseName(image string) string {
	// Find last '/' and last ':' to distinguish port from tag
	lastSlash := strings.LastIndex(image, "/")
	lastColon := strings.LastIndex(image, ":")

	imageWithoutTag := image
	if lastColon > lastSlash {
		imageWithoutTag = image[:lastColon]
	}

	// Split by '/'
	parts := strings.Split(imageWithoutTag, "/")

	var registryOrNamespace, name string
	if len(parts) == 1 {
		name = parts[0]
	} else if len(parts) == 2 {
		registryOrNamespace = parts[0]
		name = parts[1]
	} else if len(parts) > 2 {
		registryOrNamespace = parts[len(parts)-2]
		name = parts[len(parts)-1]
	}

	// Strip the port from registryOrNamespace if it looks like host:port
	if strings.Contains(registryOrNamespace, ":") {
		registryOrNamespace = strings.SplitN(registryOrNamespace, ":", 2)[0]
	}

	// Construct the base name using the sanitized registryOrNamespace and name
	var base string
	if registryOrNamespace != "" {
		base = registryOrNamespace + "-" + name
	} else {
		base = name
	}

	// Sanitize: allow alphanumeric and dashes
	var sanitizedName strings.Builder
	for _, c := range base {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' {
			sanitizedName.WriteRune(c)
		} else {
			sanitizedName.WriteRune('-')
		}
	}

	return sanitizedName.String()
}

// appendTimestamp appends a timestamp to a base name to ensure uniqueness
func appendTimestamp(baseName string) string {
	timestamp := time.Now().Unix()
	return fmt.Sprintf("%s-%d", baseName, timestamp)
}
