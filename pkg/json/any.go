// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package json provides JSON-related utilities for ToolHive.
//
// This package extends Go's standard json package with types that work
// seamlessly with both Kubernetes CRDs and CLI YAML configurations.
package json

import (
	stdjson "encoding/json"
	"fmt"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/runtime"
)

// Data stores JSON-compatible data of type T. It supports both JSON and YAML
// marshaling/unmarshaling, making it suitable for use in both Kubernetes CRDs
// and CLI YAML configurations.
//
// The Value field stores the Go value directly, which simplifies usage in tests
// and when working with the data programmatically.
//
// Common instantiations:
//   - Data[any] (aliased as Any) for arbitrary JSON values
//   - Data[map[string]any] (aliased as Map) for JSON objects
//
// +kubebuilder:pruning:PreserveUnknownFields
// +kubebuilder:validation:Type=object
type Data[T any] struct {
	// Value holds the typed Go value.
	Value T `json:"-" yaml:"-"`
}

// MarshalJSON implements json.Marshaler.
func (d Data[T]) MarshalJSON() ([]byte, error) {
	if d.IsEmpty() {
		return []byte("null"), nil
	}
	return stdjson.Marshal(d.Value)
}

// UnmarshalJSON implements json.Unmarshaler.
func (d *Data[T]) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		var zero T
		d.Value = zero
		return nil
	}
	var v T
	if err := stdjson.Unmarshal(data, &v); err != nil {
		return err
	}
	d.Value = v
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Data[T]) MarshalYAML() (interface{}, error) {
	return d.Value, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Data[T]) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		var zero T
		d.Value = zero
		return nil
	}

	var value T
	if err := node.Decode(&value); err != nil {
		return err
	}
	d.Value = value
	return nil
}

// Get returns the stored value.
func (d Data[T]) Get() T {
	return d.Value
}

// IsEmpty returns true if the value is nil or empty.
// For maps and slices, it checks if the length is 0.
func (d Data[T]) IsEmpty() bool {
	v := any(d.Value)
	if v == nil {
		return true
	}
	// Check for empty maps and slices
	switch val := v.(type) {
	case map[string]any:
		return len(val) == 0
	case []any:
		return len(val) == 0
	}
	return false
}

// DeepCopyInto copies the receiver into out. Required for controller-gen.
func (d *Data[T]) DeepCopyInto(out *Data[T]) {
	if any(d.Value) != nil {
		// Deep copy Value by marshaling and unmarshaling
		raw, err := stdjson.Marshal(d.Value)
		if err != nil {
			panic(fmt.Sprintf("failed to marshal Data[%T]: %v", d.Value, err))
		}

		var v T
		if err := stdjson.Unmarshal(raw, &v); err != nil {
			panic(fmt.Sprintf("failed to unmarshal Data[%T]: %v", d.Value, err))
		}
		out.Value = v
	} else {
		var zero T
		out.Value = zero
	}
}

// DeepCopy creates a deep copy. Required for controller-gen.
func (d *Data[T]) DeepCopy() *Data[T] {
	if d == nil {
		return nil
	}
	out := new(Data[T])
	d.DeepCopyInto(out)
	return out
}

// Any is a type alias for Data[any], storing arbitrary JSON values.
// This is the most flexible type, suitable when the JSON structure is unknown.
//
// +kubebuilder:pruning:PreserveUnknownFields
// +kubebuilder:validation:Type=object
type Any = Data[any]

// Map is a type alias for Data[map[string]any], storing JSON objects.
// Use this when you know the data will always be a JSON object (not array, string, etc.).
//
// +kubebuilder:pruning:PreserveUnknownFields
// +kubebuilder:validation:Type=object
type Map = Data[map[string]any]

// NewData creates a Data[T] from a value.
func NewData[T any](v T) Data[T] {
	return Data[T]{Value: v}
}

// NewAny creates an Any (Data[any]) from a value.
// This is a convenience function for tests and programmatic use.
func NewAny(v any) Any {
	return Any{Value: v}
}

// NewMap creates a Map (Data[map[string]any]) from a map.
func NewMap(m map[string]any) Map {
	return Map{Value: m}
}

// MustParse parses a JSON string into an Any.
// This is a convenience function for tests. Panics if parsing fails.
func MustParse(jsonStr string) Any {
	var v any
	if err := stdjson.Unmarshal([]byte(jsonStr), &v); err != nil {
		panic(fmt.Sprintf("json.MustParse: failed to parse JSON: %v", err))
	}
	return Any{Value: v}
}

// FromRawExtension creates an Any from runtime.RawExtension.
// Returns an error if the JSON cannot be unmarshaled.
func FromRawExtension(ext runtime.RawExtension) (Any, error) {
	if len(ext.Raw) == 0 {
		return Any{}, nil
	}
	var v any
	if err := stdjson.Unmarshal(ext.Raw, &v); err != nil {
		return Any{}, fmt.Errorf("failed to unmarshal RawExtension: %w", err)
	}
	return Any{Value: v}, nil
}

// MapFromRawExtension creates a Map from runtime.RawExtension.
// Returns an error if the JSON cannot be unmarshaled.
func MapFromRawExtension(ext runtime.RawExtension) (Map, error) {
	if len(ext.Raw) == 0 {
		return Map{}, nil
	}
	var v map[string]any
	if err := stdjson.Unmarshal(ext.Raw, &v); err != nil {
		return Map{}, fmt.Errorf("failed to unmarshal RawExtension as map: %w", err)
	}
	return Map{Value: v}, nil
}

// ToMap returns the data as a map[string]any.
// This is a convenience method for Any types.
// Returns nil if there is no data or if the data is not a map.
func (d Data[T]) ToMap() (map[string]any, error) {
	if any(d.Value) == nil {
		return nil, nil
	}
	if m, ok := any(d.Value).(map[string]any); ok {
		return m, nil
	}
	// Data is set but not a map - marshal and unmarshal to convert
	raw, err := stdjson.Marshal(d.Value)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := stdjson.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ToAny returns the data as any type.
// This is useful when you need to pass the value to functions expecting any.
func (d Data[T]) ToAny() (any, error) {
	return d.Value, nil
}
