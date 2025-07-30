package e2e

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestE2e(t *testing.T) { //nolint:paralleltest // E2E tests should not run in parallel
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2e Suite")
}
