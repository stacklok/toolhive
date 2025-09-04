package errors

import (
	"errors"
	"testing"
)

func TestError_Error(t *testing.T) {
	tests := []struct {
		name    string
		err     *Error
		want    string
	}{
		{
			name: "error with cause",
			err: &Error{
				Type:    ErrInvalidArgument,
				Message: "test message",
				Cause:   errors.New("underlying error"),
			},
			want: "invalid_argument: test message: underlying error",
		},
		{
			name: "error without cause",
			err: &Error{
				Type:    ErrContainerRuntime,
				Message: "test message",
				Cause:   nil,
			},
			want: "container_runtime: test message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("Error.Error() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestError_Unwrap(t *testing.T) {
	cause := errors.New("underlying error")
	err := &Error{
		Type:    ErrInternal,
		Message: "test message",
		Cause:   cause,
	}

	if got := err.Unwrap(); got != cause {
		t.Errorf("Error.Unwrap() = %v, want %v", got, cause)
	}

	errNoCause := &Error{
		Type:    ErrInternal,
		Message: "test message",
		Cause:   nil,
	}

	if got := errNoCause.Unwrap(); got != nil {
		t.Errorf("Error.Unwrap() = %v, want nil", got)
	}
}

func TestNewError(t *testing.T) {
	cause := errors.New("underlying error")
	err := NewError(ErrInvalidArgument, "test message", cause)

	if err.Type != ErrInvalidArgument {
		t.Errorf("NewError().Type = %v, want %v", err.Type, ErrInvalidArgument)
	}
	if err.Message != "test message" {
		t.Errorf("NewError().Message = %v, want %v", err.Message, "test message")
	}
	if err.Cause != cause {
		t.Errorf("NewError().Cause = %v, want %v", err.Cause, cause)
	}
}

func TestNewErrorConstructors(t *testing.T) {
	cause := errors.New("cause")

	tests := []struct {
		name        string
		constructor func(string, error) *Error
		wantType    string
	}{
		{
			name:        "NewInvalidArgumentError",
			constructor: NewInvalidArgumentError,
			wantType:    ErrInvalidArgument,
		},
		{
			name:        "NewContainerRuntimeError",
			constructor: NewContainerRuntimeError,
			wantType:    ErrContainerRuntime,
		},
		{
			name:        "NewContainerNotFoundError",
			constructor: NewContainerNotFoundError,
			wantType:    ErrContainerNotFound,
		},
		{
			name:        "NewContainerAlreadyExistsError",
			constructor: NewContainerAlreadyExistsError,
			wantType:    ErrContainerAlreadyExists,
		},
		{
			name:        "NewContainerNotRunningError",
			constructor: NewContainerNotRunningError,
			wantType:    ErrContainerNotRunning,
		},
		{
			name:        "NewContainerAlreadyRunningError",
			constructor: NewContainerAlreadyRunningError,
			wantType:    ErrContainerAlreadyRunning,
		},
		{
			name:        "NewRunConfigNotFoundError",
			constructor: NewRunConfigNotFoundError,
			wantType:    ErrRunConfigNotFound,
		},
		{
			name:        "NewGroupAlreadyExistsError",
			constructor: NewGroupAlreadyExistsError,
			wantType:    ErrGroupAlreadyExists,
		},
		{
			name:        "NewGroupNotFoundError",
			constructor: NewGroupNotFoundError,
			wantType:    ErrGroupNotFound,
		},
		{
			name:        "NewTransportError",
			constructor: NewTransportError,
			wantType:    ErrTransport,
		},
		{
			name:        "NewPermissionsError",
			constructor: NewPermissionsError,
			wantType:    ErrPermissions,
		},
		{
			name:        "NewInternalError",
			constructor: NewInternalError,
			wantType:    ErrInternal,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.constructor("test message", cause)
			if err.Type != tt.wantType {
				t.Errorf("%s().Type = %v, want %v", tt.name, err.Type, tt.wantType)
			}
			if err.Message != "test message" {
				t.Errorf("%s().Message = %v, want %v", tt.name, err.Message, "test message")
			}
			if err.Cause != cause {
				t.Errorf("%s().Cause = %v, want %v", tt.name, err.Cause, cause)
			}
		})
	}
}

func TestErrorTypeCheckers(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		checker func(error) bool
		want    bool
	}{
		{
			name:    "IsInvalidArgument with matching error",
			err:     NewInvalidArgumentError("test", nil),
			checker: IsInvalidArgument,
			want:    true,
		},
		{
			name:    "IsInvalidArgument with non-matching error",
			err:     NewContainerRuntimeError("test", nil),
			checker: IsInvalidArgument,
			want:    false,
		},
		{
			name:    "IsInvalidArgument with non-Error type",
			err:     errors.New("regular error"),
			checker: IsInvalidArgument,
			want:    false,
		},
		{
			name:    "IsContainerRuntime with matching error",
			err:     NewContainerRuntimeError("test", nil),
			checker: IsContainerRuntime,
			want:    true,
		},
		{
			name:    "IsContainerNotFound with matching error",
			err:     NewContainerNotFoundError("test", nil),
			checker: IsContainerNotFound,
			want:    true,
		},
		{
			name:    "IsContainerAlreadyExists with matching error",
			err:     NewContainerAlreadyExistsError("test", nil),
			checker: IsContainerAlreadyExists,
			want:    true,
		},
		{
			name:    "IsContainerNotRunning with matching error",
			err:     NewContainerNotRunningError("test", nil),
			checker: IsContainerNotRunning,
			want:    true,
		},
		{
			name:    "IsContainerAlreadyRunning with matching error",
			err:     NewContainerAlreadyRunningError("test", nil),
			checker: IsContainerAlreadyRunning,
			want:    true,
		},
		{
			name:    "IsRunConfigNotFound with matching error",
			err:     NewRunConfigNotFoundError("test", nil),
			checker: IsRunConfigNotFound,
			want:    true,
		},
		{
			name:    "IsGroupAlreadyExists with matching error",
			err:     NewGroupAlreadyExistsError("test", nil),
			checker: IsGroupAlreadyExists,
			want:    true,
		},
		{
			name:    "IsGroupNotFound with matching error",
			err:     NewGroupNotFoundError("test", nil),
			checker: IsGroupNotFound,
			want:    true,
		},
		{
			name:    "IsTransport with matching error",
			err:     NewTransportError("test", nil),
			checker: IsTransport,
			want:    true,
		},
		{
			name:    "IsPermissions with matching error",
			err:     NewPermissionsError("test", nil),
			checker: IsPermissions,
			want:    true,
		},
		{
			name:    "IsInternal with matching error",
			err:     NewInternalError("test", nil),
			checker: IsInternal,
			want:    true,
		},
		{
			name:    "IsInternal with nil error",
			err:     nil,
			checker: IsInternal,
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.checker(tt.err)
			if got != tt.want {
				t.Errorf("%s() = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}