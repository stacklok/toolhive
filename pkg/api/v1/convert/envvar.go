// Package convert provides conversion functions between API types and internal types.
package convert

import (
	"github.com/StacklokLabs/toolhive/pkg/api/v1"
	"github.com/StacklokLabs/toolhive/pkg/registry"
)

// EnvVarsFromInternal converts registry.EnvVar slice to API v1.EnvVar slice.
func EnvVarsFromInternal(envVars []*registry.EnvVar) []*v1.EnvVar {
	if envVars == nil {
		return nil
	}

	result := make([]*v1.EnvVar, len(envVars))
	for i, env := range envVars {
		result[i] = &v1.EnvVar{
			Name:        env.Name,
			Description: env.Description,
			Required:    env.Required,
			Default:     env.Default,
		}
	}

	return result
}

// EnvVarsToInternal converts API v1.EnvVar slice to registry.EnvVar slice.
func EnvVarsToInternal(envVars []*v1.EnvVar) []*registry.EnvVar {
	if envVars == nil {
		return nil
	}

	result := make([]*registry.EnvVar, len(envVars))
	for i, env := range envVars {
		result[i] = &registry.EnvVar{
			Name:        env.Name,
			Description: env.Description,
			Required:    env.Required,
			Default:     env.Default,
		}
	}

	return result
}

// EnvVarsMapToSlice converts a map of environment variables to a slice of strings.
func EnvVarsMapToSlice(envVars map[string]string) []string {
	if envVars == nil {
		return nil
	}

	result := make([]string, 0, len(envVars))
	for key, value := range envVars {
		result = append(result, key+"="+value)
	}

	return result
}

// EnvVarsSliceToMap converts a slice of environment variable strings to a map.
func EnvVarsSliceToMap(envVars []string) map[string]string {
	if envVars == nil {
		return nil
	}

	result := make(map[string]string, len(envVars))
	for _, env := range envVars {
		// Find the first equals sign
		for i := 0; i < len(env); i++ {
			if env[i] == '=' {
				key := env[:i]
				value := ""
				if i+1 < len(env) {
					value = env[i+1:]
				}
				result[key] = value
				break
			}
		}
	}

	return result
}
