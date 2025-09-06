package errors

import (
	"errors"
	"testing"
)

func TestErrUnsupportedTransport(t *testing.T) {
	t.Parallel()
	if ErrUnsupportedTransport == nil {
		t.Error("ErrUnsupportedTransport should not be nil")
	}

	expectedMsg := "unsupported transport type"
	if ErrUnsupportedTransport.Error() != expectedMsg {
		t.Errorf("ErrUnsupportedTransport.Error() = %v, want %v", ErrUnsupportedTransport.Error(), expectedMsg)
	}

	// Test that it's a distinct error
	if errors.Is(ErrUnsupportedTransport, ErrContainerNameNotSet) {
		t.Error("ErrUnsupportedTransport should not be equal to ErrContainerNameNotSet")
	}

	// Test error wrapping
	wrappedErr := errors.Join(ErrUnsupportedTransport, errors.New("additional context"))
	if !errors.Is(wrappedErr, ErrUnsupportedTransport) {
		t.Error("Wrapped error should still match ErrUnsupportedTransport")
	}
}

func TestErrContainerNameNotSet(t *testing.T) {
	t.Parallel()
	if ErrContainerNameNotSet == nil {
		t.Error("ErrContainerNameNotSet should not be nil")
	}

	expectedMsg := "container name not set"
	if ErrContainerNameNotSet.Error() != expectedMsg {
		t.Errorf("ErrContainerNameNotSet.Error() = %v, want %v", ErrContainerNameNotSet.Error(), expectedMsg)
	}

	// Test that it's a distinct error
	if errors.Is(ErrContainerNameNotSet, ErrUnsupportedTransport) {
		t.Error("ErrContainerNameNotSet should not be equal to ErrUnsupportedTransport")
	}

	// Test error wrapping
	wrappedErr := errors.Join(ErrContainerNameNotSet, errors.New("additional context"))
	if !errors.Is(wrappedErr, ErrContainerNameNotSet) {
		t.Error("Wrapped error should still match ErrContainerNameNotSet")
	}
}
