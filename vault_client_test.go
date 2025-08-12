package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestConvertToMap(t *testing.T) {
	tests := []struct {
		name    string
		input   interface{}
		want    map[string]interface{}
		wantErr bool
	}{
		{
			name:  "already a map",
			input: map[string]interface{}{"key": "value"},
			want:  map[string]interface{}{"key": "value"},
		},
		{
			name: "struct with json tags",
			input: struct {
				Field1 string `json:"field1"`
				Field2 int    `json:"field2"`
			}{
				Field1: "test",
				Field2: 42,
			},
			want: map[string]interface{}{"field1": "test", "field2": float64(42)},
		},
		{
			name: "nested struct",
			input: struct {
				Outer string `json:"outer"`
				Inner struct {
					Nested string `json:"nested"`
				} `json:"inner"`
			}{
				Outer: "test",
				Inner: struct {
					Nested string `json:"nested"`
				}{
					Nested: "value",
				},
			},
			want: map[string]interface{}{
				"outer": "test",
				"inner": map[string]interface{}{
					"nested": "value",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := convertToMap(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("convertToMap() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			// Compare as JSON strings for easier debugging
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(tt.want)

			if string(gotJSON) != string(wantJSON) {
				t.Errorf("convertToMap() = %s, want %s", gotJSON, wantJSON)
			}
		})
	}
}

// Integration test - requires a running Vault instance
// Run with: VAULT_ADDR=http://127.0.0.1:8200 go test -v -run TestVaultClientIntegration
func TestVaultClientIntegration(t *testing.T) {
	// Skip if VAULT_ADDR is not set
	if os.Getenv("VAULT_ADDR") == "" {
		t.Skip("Skipping integration test: VAULT_ADDR not set")
	}

	// This would require a test Vault instance
	// For now, we'll just test that the client can be created
	client, err := NewVaultClient("http://127.0.0.1:8200", "", true)
	if err != nil {
		t.Fatalf("Failed to create vault client: %v", err)
	}

	if client == nil {
		t.Fatal("Expected non-nil client")
	}
}
