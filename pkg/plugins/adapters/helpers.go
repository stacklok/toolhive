// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package adapters

import "github.com/stacklok/toolhive/pkg/plugins"

// droppedComponents returns the component types declared in the inventory that
// are not present in the supported set.
func droppedComponents(inv plugins.ComponentInventory, supported []plugins.ComponentType) []plugins.ComponentType {
	if inv == nil {
		return nil
	}
	set := make(map[plugins.ComponentType]struct{}, len(supported))
	for _, ct := range supported {
		set[ct] = struct{}{}
	}
	var out []plugins.ComponentType
	for key := range inv {
		ct := plugins.ComponentType(key)
		if _, ok := set[ct]; !ok {
			out = append(out, ct)
		}
	}
	return out
}
