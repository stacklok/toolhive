// Package convert provides conversion functions between API types and internal types.
//
// This package contains functions to convert between the API types defined in the
// v1 package and the internal types used by the ToolHive implementation. This
// separation allows the API to evolve independently from the implementation.
//
// The conversion functions are organized by type:
//   - server.go: Conversions for Server types
//   - permission.go: Conversions for PermissionProfile types
//   - envvar.go: Conversions for EnvVar types
//   - runoptions.go: Conversions for RunOptions types
//
// Each file contains functions to convert from internal types to API types and vice versa.
package convert
