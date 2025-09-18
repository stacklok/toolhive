package sync

// IsManualSync checks if the sync reason indicates a manual sync
func IsManualSync(reason string) bool {
	return reason == ReasonManualWithChanges || reason == ReasonManualNoChanges
}
