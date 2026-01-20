// Package composer provides composite tool workflow execution for Virtual MCP Server.
package composer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	thvjson "github.com/stacklok/toolhive/pkg/json"
	"github.com/stacklok/toolhive/pkg/logger"
	"github.com/stacklok/toolhive/pkg/vmcp/config"
)

const (
	// Type constants for output properties
	typeString  = "string"
	typeInteger = "integer"
	typeNumber  = "number"
	typeBoolean = "boolean"
	typeObject  = "object"
	typeArray   = "array"
)

// constructOutputFromConfig builds the workflow output from the output configuration.
// This expands templates in the Value fields, deserializes JSON for object types,
// applies default values on expansion failure, and validates the final output.
func (e *workflowEngine) constructOutputFromConfig(
	ctx context.Context,
	outputConfig *config.OutputConfig,
	workflowCtx *WorkflowContext,
) (map[string]any, error) {
	if outputConfig == nil {
		return nil, fmt.Errorf("output config is nil")
	}

	output := make(map[string]any)

	// Construct each output property
	for propertyName, propertyDef := range outputConfig.Properties {
		value, err := e.constructOutputProperty(ctx, propertyName, propertyDef, workflowCtx)
		if err != nil {
			return nil, fmt.Errorf("failed to construct output property %q: %w", propertyName, err)
		}
		output[propertyName] = value
	}

	// Validate required fields are present and non-nil
	if len(outputConfig.Required) > 0 {
		for _, requiredField := range outputConfig.Required {
			value, exists := output[requiredField]
			if !exists {
				return nil, fmt.Errorf("required output field %q is missing", requiredField)
			}
			if value == nil {
				return nil, fmt.Errorf("required output field %q is nil", requiredField)
			}
		}
	}

	return output, nil
}

// constructOutputProperty constructs a single output property value.
func (e *workflowEngine) constructOutputProperty(
	ctx context.Context,
	propertyName string,
	propertyDef config.OutputProperty,
	workflowCtx *WorkflowContext,
) (any, error) {
	// Check if we should construct from Value or Properties
	hasValue := propertyDef.Value != ""
	hasProperties := len(propertyDef.Properties) > 0

	if hasValue {
		return e.constructOutputPropertyFromValue(ctx, propertyName, propertyDef, workflowCtx)
	} else if hasProperties {
		return e.constructOutputPropertyFromProperties(ctx, propertyName, propertyDef, workflowCtx)
	}

	// This shouldn't happen if validation passed, but handle it
	return nil, fmt.Errorf("property %q has neither value nor properties", propertyName)
}

// constructOutputPropertyFromValue constructs a property value from a template.
func (e *workflowEngine) constructOutputPropertyFromValue(
	ctx context.Context,
	propertyName string,
	propertyDef config.OutputProperty,
	workflowCtx *WorkflowContext,
) (any, error) {
	// Expand the template using a map wrapper
	templateMap := map[string]any{"_value": propertyDef.Value}
	expanded, err := e.templateExpander.Expand(ctx, templateMap, workflowCtx)
	if err != nil {
		// Template expansion failed - try to use default value
		if !propertyDef.Default.IsEmpty() {
			logger.Warnf("Failed to expand template for property %q: %v. Using default value.", propertyName, err)
			return e.coerceRawJSONDefaultValue(propertyDef.Default, propertyDef.Type)
		}
		// No default - propagate error
		return nil, fmt.Errorf("failed to expand template for property %q: %w", propertyName, err)
	}

	// Extract the expanded string value
	expandedVal := expanded["_value"]
	expandedStr, ok := expandedVal.(string)
	if !ok {
		// If it's not a string, it might already be the right type from template expansion
		return expandedVal, nil
	}

	// Check if template expansion returned "<no value>" placeholder (missing field)
	// In this case, fallback to default value if available
	if expandedStr == "<no value>" && !propertyDef.Default.IsEmpty() {
		logger.Warnf("Template expanded to <no value> for property %q, using default value", propertyName)
		return e.coerceRawJSONDefaultValue(propertyDef.Default, propertyDef.Type)
	}

	// For object types, attempt JSON deserialization
	// Note, the following type coercion is duplicative with the tool call type coercion
	// from the schema package.
	// TODO: Refactor the two to use one implementation.
	if propertyDef.Type == typeObject {
		var obj map[string]any
		if err := json.Unmarshal([]byte(expandedStr), &obj); err != nil {
			// JSON deserialization failed - try default value
			if !propertyDef.Default.IsEmpty() {
				logger.Warnf("Failed to deserialize JSON for property %q: %v. Using default value.", propertyName, err)
				return e.coerceRawJSONDefaultValue(propertyDef.Default, propertyDef.Type)
			}
			return nil, fmt.Errorf("failed to deserialize JSON for object property %q: %w", propertyName, err)
		}
		return obj, nil
	}

	// For array types, attempt JSON deserialization
	if propertyDef.Type == typeArray {
		var arr []any
		if err := json.Unmarshal([]byte(expandedStr), &arr); err != nil {
			// JSON deserialization failed - try default value
			if !propertyDef.Default.IsEmpty() {
				logger.Warnf("Failed to deserialize JSON array for property %q: %v. Using default value.", propertyName, err)
				return e.coerceRawJSONDefaultValue(propertyDef.Default, propertyDef.Type)
			}
			return nil, fmt.Errorf("failed to deserialize JSON array for property %q: %w", propertyName, err)
		}
		return arr, nil
	}

	// For other types, coerce the string to the appropriate type
	typedValue, err := e.coerceStringToType(expandedStr, propertyDef.Type)
	if err != nil {
		// Type coercion failed - try default value
		if !propertyDef.Default.IsEmpty() {
			logger.Warnf("Failed to coerce value for property %q: %v. Using default value.", propertyName, err)
			return e.coerceRawJSONDefaultValue(propertyDef.Default, propertyDef.Type)
		}
		return nil, fmt.Errorf("failed to coerce value for property %q: %w", propertyName, err)
	}

	return typedValue, nil
}

// constructOutputPropertyFromProperties constructs a property value from nested properties.
func (e *workflowEngine) constructOutputPropertyFromProperties(
	ctx context.Context,
	propertyName string,
	propertyDef config.OutputProperty,
	workflowCtx *WorkflowContext,
) (any, error) {
	// Recursively construct nested object
	nestedObj := make(map[string]any)

	for nestedName, nestedDef := range propertyDef.Properties {
		nestedValue, err := e.constructOutputProperty(
			ctx,
			fmt.Sprintf("%s.%s", propertyName, nestedName),
			nestedDef,
			workflowCtx,
		)
		if err != nil {
			return nil, err
		}
		nestedObj[nestedName] = nestedValue
	}

	return nestedObj, nil
}

// coerceStringToType converts a string value to the specified type.
func (*workflowEngine) coerceStringToType(value string, targetType string) (any, error) {
	switch targetType {
	case typeString:
		return value, nil

	case typeInteger:
		// Try to parse as integer
		intVal, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("cannot coerce %q to integer: %w", value, err)
		}
		return intVal, nil

	case typeNumber:
		// Try to parse as float
		floatVal, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return nil, fmt.Errorf("cannot coerce %q to number: %w", value, err)
		}
		return floatVal, nil

	case typeBoolean:
		// Try to parse as boolean
		b, err := strconv.ParseBool(value)
		if err != nil {
			return nil, fmt.Errorf("cannot coerce %q to boolean: %w", value, err)
		}
		return b, nil

	default:
		return nil, fmt.Errorf("unsupported type for string coercion: %s", targetType)
	}
}

// coerceRawJSONDefaultValue extracts value from json.Any and coerces it to the target type.
func (e *workflowEngine) coerceRawJSONDefaultValue(defaultVal thvjson.Any, targetType string) (any, error) {
	value, err := defaultVal.ToAny()
	if err != nil {
		return nil, fmt.Errorf("failed to extract default value: %w", err)
	}
	return e.coerceDefaultValue(value, targetType)
}

// coerceDefaultValue coerces a default value to the target type.
// This handles type coercion from various input types (especially for YAML/JSON parsing).
//
//nolint:gocyclo // Type coercion naturally has many branches
func (*workflowEngine) coerceDefaultValue(defaultVal any, targetType string) (any, error) {
	// If default is nil, return nil
	if defaultVal == nil {
		return nil, nil
	}

	// If default is already the correct type, return as-is
	switch targetType {
	case typeString:
		if str, ok := defaultVal.(string); ok {
			return str, nil
		}
		// Convert other types to string
		return fmt.Sprintf("%v", defaultVal), nil

	case typeInteger:
		// Handle various integer representations
		switch v := defaultVal.(type) {
		case int:
			return int64(v), nil
		case int32:
			return int64(v), nil
		case int64:
			return v, nil
		case float64:
			// Check for potential truncation
			intVal := int64(v)
			if float64(intVal) != v {
				logger.Warnf("Potential precision loss converting float64 %v to int64 %d", v, intVal)
			}
			return intVal, nil
		case float32:
			// Check for potential truncation
			intVal := int64(v)
			if float32(intVal) != v {
				logger.Warnf("Potential precision loss converting float32 %v to int64 %d", v, intVal)
			}
			return intVal, nil
		case string:
			return strconv.ParseInt(v, 10, 64)
		default:
			return nil, fmt.Errorf("cannot coerce default value %v (type %T) to integer", defaultVal, defaultVal)
		}

	case typeNumber:
		// Handle various number representations
		switch v := defaultVal.(type) {
		case float64:
			return v, nil
		case float32:
			return float64(v), nil
		case int:
			return float64(v), nil
		case int32:
			return float64(v), nil
		case int64:
			return float64(v), nil
		case string:
			return strconv.ParseFloat(v, 64)
		default:
			return nil, fmt.Errorf("cannot coerce default value %v (type %T) to number", defaultVal, defaultVal)
		}

	case typeBoolean:
		switch v := defaultVal.(type) {
		case bool:
			return v, nil
		case string:
			switch v {
			case "true", "True", "TRUE", "1": //nolint:goconst // Boolean literals are clearer than constants
				return true, nil
			case "false", "False", "FALSE", "0":
				return false, nil
			default:
				return nil, fmt.Errorf("cannot coerce string %q to boolean", v)
			}
		case int, int32, int64:
			intVal := fmt.Sprintf("%v", v)
			return intVal == "1", nil
		default:
			return nil, fmt.Errorf("cannot coerce default value %v (type %T) to boolean", defaultVal, defaultVal)
		}

	case typeObject:
		// For objects, accept maps or JSON strings
		if objMap, ok := defaultVal.(map[string]any); ok {
			return objMap, nil
		}
		if str, ok := defaultVal.(string); ok {
			var obj map[string]any
			if err := json.Unmarshal([]byte(str), &obj); err != nil {
				return nil, fmt.Errorf("cannot parse default value as JSON object: %w", err)
			}
			return obj, nil
		}
		return nil, fmt.Errorf("cannot coerce default value %v (type %T) to object", defaultVal, defaultVal)

	case typeArray:
		// For arrays, accept slices or JSON strings
		if arr, ok := defaultVal.([]any); ok {
			return arr, nil
		}
		if str, ok := defaultVal.(string); ok {
			var arr []any
			if err := json.Unmarshal([]byte(str), &arr); err != nil {
				return nil, fmt.Errorf("cannot parse default value as JSON array: %w", err)
			}
			return arr, nil
		}
		return nil, fmt.Errorf("cannot coerce default value %v (type %T) to array", defaultVal, defaultVal)

	default:
		return nil, fmt.Errorf("unsupported target type: %s", targetType)
	}
}
