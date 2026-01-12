package models

import (
	"testing"
)

func TestTransportType_Valid(t *testing.T) {
	tests := []struct {
		name      string
		transport TransportType
		want      bool
	}{
		{
			name:      "SSE transport is valid",
			transport: TransportSSE,
			want:      true,
		},
		{
			name:      "Streamable transport is valid",
			transport: TransportStreamable,
			want:      true,
		},
		{
			name:      "Invalid transport is not valid",
			transport: TransportType("invalid"),
			want:      false,
		},
		{
			name:      "Empty transport is not valid",
			transport: TransportType(""),
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.transport.Valid(); got != tt.want {
				t.Errorf("TransportType.Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTransportType_Value(t *testing.T) {
	tests := []struct {
		name      string
		transport TransportType
		wantValue string
		wantErr   bool
	}{
		{
			name:      "SSE transport value",
			transport: TransportSSE,
			wantValue: "sse",
			wantErr:   false,
		},
		{
			name:      "Streamable transport value",
			transport: TransportStreamable,
			wantValue: "streamable-http",
			wantErr:   false,
		},
		{
			name:      "Invalid transport returns error",
			transport: TransportType("invalid"),
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.transport.Value()
			if (err != nil) != tt.wantErr {
				t.Errorf("TransportType.Value() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.wantValue {
				t.Errorf("TransportType.Value() = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

func TestTransportType_Scan(t *testing.T) {
	tests := []struct {
		name      string
		value     interface{}
		want      TransportType
		wantErr   bool
	}{
		{
			name:    "Scan SSE transport",
			value:   "sse",
			want:    TransportSSE,
			wantErr: false,
		},
		{
			name:    "Scan streamable transport",
			value:   "streamable-http",
			want:    TransportStreamable,
			wantErr: false,
		},
		{
			name:    "Scan invalid transport returns error",
			value:   "invalid",
			wantErr: true,
		},
		{
			name:    "Scan nil returns error",
			value:   nil,
			wantErr: true,
		},
		{
			name:    "Scan non-string returns error",
			value:   123,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var transport TransportType
			err := transport.Scan(tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("TransportType.Scan() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && transport != tt.want {
				t.Errorf("TransportType.Scan() = %v, want %v", transport, tt.want)
			}
		})
	}
}

func TestMCPStatus_Valid(t *testing.T) {
	tests := []struct {
		name   string
		status MCPStatus
		want   bool
	}{
		{
			name:   "Running status is valid",
			status: StatusRunning,
			want:   true,
		},
		{
			name:   "Stopped status is valid",
			status: StatusStopped,
			want:   true,
		},
		{
			name:   "Invalid status is not valid",
			status: MCPStatus("invalid"),
			want:   false,
		},
		{
			name:   "Empty status is not valid",
			status: MCPStatus(""),
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.status.Valid(); got != tt.want {
				t.Errorf("MCPStatus.Valid() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMCPStatus_Value(t *testing.T) {
	tests := []struct {
		name      string
		status    MCPStatus
		wantValue string
		wantErr   bool
	}{
		{
			name:      "Running status value",
			status:    StatusRunning,
			wantValue: "running",
			wantErr:   false,
		},
		{
			name:      "Stopped status value",
			status:    StatusStopped,
			wantValue: "stopped",
			wantErr:   false,
		},
		{
			name:    "Invalid status returns error",
			status:  MCPStatus("invalid"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.status.Value()
			if (err != nil) != tt.wantErr {
				t.Errorf("MCPStatus.Value() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.wantValue {
				t.Errorf("MCPStatus.Value() = %v, want %v", got, tt.wantValue)
			}
		})
	}
}

func TestMCPStatus_Scan(t *testing.T) {
	tests := []struct {
		name    string
		value   interface{}
		want    MCPStatus
		wantErr bool
	}{
		{
			name:    "Scan running status",
			value:   "running",
			want:    StatusRunning,
			wantErr: false,
		},
		{
			name:    "Scan stopped status",
			value:   "stopped",
			want:    StatusStopped,
			wantErr: false,
		},
		{
			name:    "Scan invalid status returns error",
			value:   "invalid",
			wantErr: true,
		},
		{
			name:    "Scan nil returns error",
			value:   nil,
			wantErr: true,
		},
		{
			name:    "Scan non-string returns error",
			value:   123,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var status MCPStatus
			err := status.Scan(tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("MCPStatus.Scan() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && status != tt.want {
				t.Errorf("MCPStatus.Scan() = %v, want %v", status, tt.want)
			}
		})
	}
}


