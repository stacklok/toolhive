package filtering

import (
	"context"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "github.com/stacklok/toolhive/cmd/thv-operator/api/v1alpha1"
	"github.com/stacklok/toolhive/pkg/registry"
)

// FilterService coordinates name and tag filtering to apply registry filters
type FilterService interface {
	// ApplyFilters filters the registry based on MCPRegistry filter configuration
	ApplyFilters(ctx context.Context, reg *registry.Registry, filter *mcpv1alpha1.RegistryFilter) (*registry.Registry, error)
}

// DefaultFilterService implements filtering coordination using name and tag filters
type DefaultFilterService struct {
	nameFilter NameFilter
	tagFilter  TagFilter
}

// NewDefaultFilterService creates a new DefaultFilterService with default filter implementations
func NewDefaultFilterService() *DefaultFilterService {
	return &DefaultFilterService{
		nameFilter: NewDefaultNameFilter(),
		tagFilter:  NewDefaultTagFilter(),
	}
}

// NewFilterService creates a new DefaultFilterService with custom filter implementations
func NewFilterService(nameFilter NameFilter, tagFilter TagFilter) *DefaultFilterService {
	return &DefaultFilterService{
		nameFilter: nameFilter,
		tagFilter:  tagFilter,
	}
}

// ApplyFilters filters the registry based on MCPRegistry filter configuration
//
// The filtering process:
// 1. If no filter is specified, return the original registry unchanged
// 2. Create a new registry with the same metadata but empty server maps
// 3. For each server (both container and remote), apply name and tag filtering
// 4. Only include servers that pass both name and tag filters
// 5. Return the filtered registry
func (s *DefaultFilterService) ApplyFilters(
	ctx context.Context,
	reg *registry.Registry,
	filter *mcpv1alpha1.RegistryFilter) (*registry.Registry, error) {
	ctxLogger := log.FromContext(ctx)

	// If no filter is specified, return original registry
	if filter == nil {
		ctxLogger.V(1).Info("No filter specified, returning original registry")
		return reg, nil
	}

	ctxLogger.Info("Applying registry filters",
		"originalServerCount", len(reg.Servers),
		"originalRemoteServerCount", len(reg.RemoteServers))

	// Create a new filtered registry with same metadata
	filteredRegistry := &registry.Registry{
		Version:       reg.Version,
		LastUpdated:   reg.LastUpdated,
		Servers:       make(map[string]*registry.ImageMetadata),
		RemoteServers: make(map[string]*registry.RemoteServerMetadata),
		Groups:        reg.Groups, // Groups are not filtered for now
	}

	// Extract filter criteria
	var nameInclude, nameExclude, tagInclude, tagExclude []string
	if filter.NameFilters != nil {
		nameInclude = filter.NameFilters.Include
		nameExclude = filter.NameFilters.Exclude
	}
	if filter.Tags != nil {
		tagInclude = filter.Tags.Include
		tagExclude = filter.Tags.Exclude
	}

	includedCount := 0
	excludedCount := 0

	// Filter container servers
	for serverName, serverMetadata := range reg.Servers {
		included, reason := s.shouldIncludeServerWithReason(
			serverName,
			serverMetadata.Tags,
			nameInclude,
			nameExclude,
			tagInclude,
			tagExclude,
		)
		if included {
			filteredRegistry.Servers[serverName] = serverMetadata
			includedCount++
			ctxLogger.V(1).Info("Including container server",
				"name", serverName,
				"tags", serverMetadata.Tags,
				"reason", reason)
		} else {
			excludedCount++
			ctxLogger.V(1).Info("Excluding container server",
				"name", serverName,
				"tags", serverMetadata.Tags,
				"reason", reason)
		}
	}

	// Filter remote servers
	for serverName, serverMetadata := range reg.RemoteServers {
		included, reason := s.shouldIncludeServerWithReason(
			serverName,
			serverMetadata.Tags,
			nameInclude,
			nameExclude,
			tagInclude,
			tagExclude,
		)
		if included {
			filteredRegistry.RemoteServers[serverName] = serverMetadata
			includedCount++
			ctxLogger.V(1).Info("Including remote server",
				"name", serverName,
				"tags", serverMetadata.Tags,
				"reason", reason)
		} else {
			excludedCount++
			ctxLogger.V(1).Info("Excluding remote server",
				"name", serverName,
				"tags", serverMetadata.Tags,
				"reason", reason)
		}
	}

	ctxLogger.Info("Registry filtering completed",
		"includedServers", includedCount,
		"excludedServers", excludedCount,
		"filteredServerCount", len(filteredRegistry.Servers),
		"filteredRemoteServerCount", len(filteredRegistry.RemoteServers))

	return filteredRegistry, nil
}

// shouldIncludeServerWithReason determines if a server should be included and provides detailed reasoning
// Both name and tag filters must pass for a server to be included
func (s *DefaultFilterService) shouldIncludeServerWithReason(
	serverName string,
	serverTags []string,
	nameInclude, nameExclude, tagInclude, tagExclude []string) (bool, string) {
	// Apply name filtering first
	nameIncluded, nameReason := s.nameFilter.ShouldInclude(serverName, nameInclude, nameExclude)
	if !nameIncluded {
		return false, fmt.Sprintf("name filter: %s", nameReason)
	}

	// Apply tag filtering
	tagIncluded, tagReason := s.tagFilter.ShouldInclude(serverTags, tagInclude, tagExclude)
	if !tagIncluded {
		return false, fmt.Sprintf("tag filter: %s", tagReason)
	}

	// Both filters passed - determine the inclusion reason
	inclusionReasons := []string{}
	if len(nameInclude) > 0 || len(nameExclude) > 0 {
		inclusionReasons = append(inclusionReasons, fmt.Sprintf("name filter: %s", nameReason))
	}
	if len(tagInclude) > 0 || len(tagExclude) > 0 {
		inclusionReasons = append(inclusionReasons, fmt.Sprintf("tag filter: %s", tagReason))
	}

	if len(inclusionReasons) == 0 {
		return true, "no filters specified, default include"
	}

	return true, "passed all filters: " + strings.Join(inclusionReasons, " AND ")
}
