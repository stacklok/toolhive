package api_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAPI(t *testing.T) { //nolint:paralleltest // E2E tests should not run in parallel
	RegisterFailHandler(Fail)
	RunSpecs(t, "API E2E Suite")
}
