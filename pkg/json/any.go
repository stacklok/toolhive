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

// Any stores arbitrary JSON-compatible data. It supports both JSON and YAML
// marshaling/unmarshaling, making it suitable for use in both Kubernetes CRDs
// and CLI YAML configurations.
//
// The Data field stores the Go value directly, which simplifies usage in tests
// and when working with the data programmatically.
//
// +kubebuilder:pruning:PreserveUnknownFields
// +kubebuilder:validation:Type=object
type Any struct {
	// Data holds the Go value (maps, slices, strings, numbers, bools, nil).
	Data any `json:"-" yaml:"-"`
}

// MarshalJSON implements json.Marshaler.
func (a Any) MarshalJSON() ([]byte, error) {
	if a.Data == nil {
		return []byte("null"), nil
	}
	return stdjson.Marshal(a.Data)
}

// UnmarshalJSON implements json.Unmarshaler.
func (a *Any) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		a.Data = nil
		return nil
	}
	var v any
	if err := stdjson.Unmarshal(data, &v); err != nil {
		return err
	}
	a.Data = v
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (a Any) MarshalYAML() (interface{}, error) {
	return a.Data, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (a *Any) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode && node.Tag == "!!null" {
		a.Data = nil
		return nil
	}

	var value any
	if err := node.Decode(&value); err != nil {
		return err
	}
	a.Data = value
	return nil
}

// ToMap returns the data as a map[string]any.
// Returns nil if there is no data or if the data is not a map.
func (a Any) ToMap() (map[string]any, error) {
	if a.Data == nil {
		return nil, nil
	}
	if m, ok := a.Data.(map[string]any); ok {
		return m, nil
	}
	// Data is set but not a map - marshal and unmarshal to convert
	raw, err := stdjson.Marshal(a.Data)
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
// Returns nil if there is no data.
func (a Any) ToAny() (any, error) {
	return a.Data, nil
}

// NewAny creates an Any directly from a value.
// This is a convenience function for tests and programmatic use.
func NewAny(v any) Any {
	return Any{Data: v}
}

// MustParse parses a JSON string into an Any.
// This is a convenience function for tests. Panics if parsing fails.
func MustParse(jsonStr string) Any {
	var v any
	if err := stdjson.Unmarshal([]byte(jsonStr), &v); err != nil {
		panic(fmt.Sprintf("json.MustParse: failed to parse JSON: %v", err))
	}
	return Any{Data: v}
}

// IsEmpty returns true if there is no data.
func (a Any) IsEmpty() bool {
	return a.Data == nil
}

// DeepCopyInto copies the receiver into out. Required for controller-gen.
func (a *Any) DeepCopyInto(out *Any) {
	if a.Data != nil {
		// Deep copy Data by marshaling and unmarshaling
		raw, err := stdjson.Marshal(a.Data)
		if err == nil {
			var v any
			if stdjson.Unmarshal(raw, &v) == nil {
				out.Data = v
			}
		}
	} else {
		out.Data = nil
	}
}

// DeepCopy creates a deep copy of Any. Required for controller-gen.
func (a *Any) DeepCopy() *Any {
	if a == nil {
		return nil
	}
	out := new(Any)
	a.DeepCopyInto(out)
	return out
}

// ToRawExtension converts Any to runtime.RawExtension for K8s compatibility.
func (a Any) ToRawExtension() runtime.RawExtension {
	if a.Data == nil {
		return runtime.RawExtension{}
	}
	raw, err := stdjson.Marshal(a.Data)
	if err != nil {
		return runtime.RawExtension{}
	}
	return runtime.RawExtension{Raw: raw}
}

// FromRawExtension creates an Any from runtime.RawExtension.
func FromRawExtension(ext runtime.RawExtension) Any {
	if len(ext.Raw) == 0 {
		return Any{}
	}
	var v any
	if err := stdjson.Unmarshal(ext.Raw, &v); err != nil {
		return Any{}
	}
	return Any{Data: v}
}
