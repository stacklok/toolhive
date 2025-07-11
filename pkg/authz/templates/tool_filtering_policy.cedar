// Tool filtering policy template
// This policy allows access to specific tools based on the --tools flag
// The tools are interpolated into this template at runtime

// Allow access to specific tools only{{range .Tools}}
permit(principal, action == Action::"call_tool", resource == Tool::"{{.}}");{{end}}

// Allow listing of tools (for tools/list operations)
permit(principal, action == Action::"list_tools", resource == FeatureType::"tool");
