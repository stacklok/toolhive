package telemetry

import (
	"testing"

	"go.opentelemetry.io/otel/attribute"
)

func TestParseCustomAttributes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    []attribute.KeyValue
		wantErr bool
	}{
		{
			name:  "single attribute",
			input: "environment=production",
			want: []attribute.KeyValue{
				attribute.String("environment", "production"),
			},
		},
		{
			name:  "multiple attributes",
			input: "environment=production,region=us-east-1,team=platform",
			want: []attribute.KeyValue{
				attribute.String("environment", "production"),
				attribute.String("region", "us-east-1"),
				attribute.String("team", "platform"),
			},
		},
		{
			name:  "attributes with spaces",
			input: " environment = production , region = us-east-1 ",
			want: []attribute.KeyValue{
				attribute.String("environment", "production"),
				attribute.String("region", "us-east-1"),
			},
		},
		{
			name:  "attribute with special characters",
			input: "service.name=my-service,service.version=1.2.3",
			want: []attribute.KeyValue{
				attribute.String("service.name", "my-service"),
				attribute.String("service.version", "1.2.3"),
			},
		},
		{
			name:  "attribute with underscore",
			input: "server_type=production,deployment_id=12345",
			want: []attribute.KeyValue{
				attribute.String("server_type", "production"),
				attribute.String("deployment_id", "12345"),
			},
		},
		{
			name:  "empty input",
			input: "",
			want:  []attribute.KeyValue{},
		},
		{
			name:  "trailing comma",
			input: "environment=production,",
			want: []attribute.KeyValue{
				attribute.String("environment", "production"),
			},
		},
		{
			name:  "multiple equals in value",
			input: "url=https://example.com/path?query=value",
			want: []attribute.KeyValue{
				attribute.String("url", "https://example.com/path?query=value"),
			},
		},
		{
			name:    "missing equals",
			input:   "environment",
			wantErr: true,
		},
		{
			name:    "missing key",
			input:   "=production",
			wantErr: true,
		},
		{
			name:    "empty key",
			input:   " =production",
			wantErr: true,
		},
		{
			name:  "empty value is allowed",
			input: "debug=",
			want: []attribute.KeyValue{
				attribute.String("debug", ""),
			},
		},
		{
			name:    "mixed valid and invalid",
			input:   "environment=production,invalid,region=us-east-1",
			wantErr: true,
		},
		{
			name:  "numeric-like values as strings",
			input: "port=8080,count=100,ratio=0.95",
			want: []attribute.KeyValue{
				attribute.String("port", "8080"),
				attribute.String("count", "100"),
				attribute.String("ratio", "0.95"),
			},
		},
		{
			name:  "boolean-like values as strings",
			input: "enabled=true,debug=false",
			want: []attribute.KeyValue{
				attribute.String("enabled", "true"),
				attribute.String("debug", "false"),
			},
		},
		{
			name:  "attribute with encoded characters",
			input: "message=Hello%20World,path=/api/v1/users",
			want: []attribute.KeyValue{
				attribute.String("message", "Hello%20World"),
				attribute.String("path", "/api/v1/users"),
			},
		},
		{
			name:  "complex real-world example",
			input: "service.name=toolhive,service.namespace=default,service.instance.id=i-1234567890abcdef,cloud.provider=aws,cloud.region=us-west-2",
			want: []attribute.KeyValue{
				attribute.String("service.name", "toolhive"),
				attribute.String("service.namespace", "default"),
				attribute.String("service.instance.id", "i-1234567890abcdef"),
				attribute.String("cloud.provider", "aws"),
				attribute.String("cloud.region", "us-west-2"),
			},
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseCustomAttributes(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseCustomAttributes() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if len(got) != len(tt.want) {
					t.Errorf("ParseCustomAttributes() got %d attributes, want %d", len(got), len(tt.want))
					return
				}

				// Check that the returned map is as expected for the input.
				gotMap := got
				wantMap := make(map[string]string)
				for _, attr := range tt.want {
					wantMap[string(attr.Key)] = attr.Value.AsString()
				}
				if len(gotMap) != len(wantMap) {
					t.Errorf("ParseCustomAttributes() got %d attributes, want %d", len(gotMap), len(wantMap))
				} else {
					for k, v := range wantMap {
						if gotMap[k] != v {
							t.Errorf("ParseCustomAttributes()[%q] = %q, want %q", k, gotMap[k], v)
						}
					}
				}
			}
		})
	}
}

func TestConvertMapToAttributes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input map[string]string
		want  []attribute.KeyValue
	}{
		{
			name:  "empty map",
			input: map[string]string{},
			want:  []attribute.KeyValue{},
		},
		{
			name:  "single attribute",
			input: map[string]string{"foo": "bar"},
			want: []attribute.KeyValue{
				attribute.String("foo", "bar"),
			},
		},
		{
			name: "multiple attributes",
			input: map[string]string{
				"env":     "prod",
				"team":    "platform",
				"release": "stable",
			},
			want: []attribute.KeyValue{
				attribute.String("env", "prod"),
				attribute.String("team", "platform"),
				attribute.String("release", "stable"),
			},
		},
	}

	for _, tt := range tests {
		tt := tt // capture range variable
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ConvertMapToAttributes(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("ConvertMapToAttributes() got %d attributes, want %d", len(got), len(tt.want))
				return
			}
			// Convert result to map for easy comparison (since map iteration order is not guaranteed)
			gotMap := make(map[attribute.Key]string)
			for _, kv := range got {
				gotMap[kv.Key] = kv.Value.AsString()
			}
			for _, wantKV := range tt.want {
				val, ok := gotMap[wantKV.Key]
				if !ok {
					t.Errorf("ConvertMapToAttributes() missing key %v", wantKV.Key)
				} else if val != wantKV.Value.AsString() {
					t.Errorf("ConvertMapToAttributes() key %v = %v, want %v", wantKV.Key, val, wantKV.Value.AsString())
				}
			}
		})
	}
}
