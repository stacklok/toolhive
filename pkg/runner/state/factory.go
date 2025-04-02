package state

// NewStore creates a new state store
// For now, only the local store is implemented
func NewStore(appName string) (Store, error) {
	return NewLocalStore(appName)
}
