package secrets

import (
	"errors"
	"fmt"

	"github.com/adrg/xdg"
)

// ManagerType represents an enum of the types of available secrets providers.
type ManagerType string

const (
	// BasicType represents the basic secret provider.
	BasicType ManagerType = "basic"
)

// ErrUnknownManagerType is returned when an invalid value for ManagerType is specified.
var ErrUnknownManagerType = errors.New("unknown secret manager type")

// CreateSecretManager creates the specified type of secret manager.
func CreateSecretManager(managerType ManagerType) (Manager, error) {
	switch managerType {
	case BasicType:
		secretsPath, err := xdg.DataFile("vibetool/secrets")
		if err != nil {
			return nil, fmt.Errorf("unable to access secrets file path %v", err)
		}
		return NewBasicManager(secretsPath)
	default:
		return nil, ErrUnknownManagerType
	}
}
