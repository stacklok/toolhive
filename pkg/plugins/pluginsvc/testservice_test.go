// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package pluginsvc

// newTestService builds a *service (the concrete type exposing the Phase-3
// Install/Uninstall/List/Info methods) from the given options. The public New
// constructor returns the full plugins.PluginService interface; tests cast to
// the concrete type to exercise package-private state.
func newTestService(opts ...Option) *service {
	return New(opts...).(*service)
}
