package controllerutil

// AppendProxyArgs appends user-specified args to the base proxy deployment args.
// Cobra parses flags regardless of position, so order doesn't matter.
//
// Parameters:
//   - baseArgs: The base arguments for the proxy (e.g., ["run", "image"])
//   - overrideArgs: User-specified arguments to append (e.g., ["--debug"])
//
// Returns the combined args slice with overrideArgs appended to baseArgs.
// Note: Kubernetes API server limits prevent excessive args (CR size limit ~1.5MB),
// and Go's append() handles capacity management safely without overflow risk.
func AppendProxyArgs(baseArgs, overrideArgs []string) []string {
	if len(overrideArgs) == 0 {
		return baseArgs
	}

	return append(baseArgs, overrideArgs...)
}
