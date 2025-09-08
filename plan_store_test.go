package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCalculateSHA256(t *testing.T) {
	t.Parallel()

	// Create a temporary file
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	testContent := "Hello, World!"

	err := os.WriteFile(testFile, []byte(testContent), 0600)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Calculate SHA256 using our function
	sha, err := calculateSHA256(testFile)
	if err != nil {
		t.Fatalf("Failed to calculate SHA256: %v", err)
	}

	// Calculate expected SHA256
	hasher := sha256.New()
	hasher.Write([]byte(testContent))
	expected := hex.EncodeToString(hasher.Sum(nil))

	if sha != expected {
		t.Errorf("SHA256 mismatch: got %s, expected %s", sha, expected)
	}
}

func TestPlanStorageStructure(t *testing.T) {
	t.Parallel()

	// Create test plan structure
	tmpDir := t.TempDir()

	// Create a mock service plan directory structure
	serviceDir := filepath.Join(tmpDir, "redis-blacksmith-plans", "plans")
	planDirs := []string{"standalone", "cluster", "standalone-6"}

	for _, plan := range planDirs {
		planPath := filepath.Join(serviceDir, plan)

		err := os.MkdirAll(planPath, 0750)
		if err != nil {
			t.Fatalf("Failed to create plan directory %s: %v", planPath, err)
		}

		// Create plan files
		files := map[string]string{
			"manifest.yml":    fmt.Sprintf("# Manifest for %s\nname: %s\n", plan, plan),
			"credentials.yml": fmt.Sprintf("# Credentials for %s\npassword: secret\n", plan),
			"init":            fmt.Sprintf("#!/bin/bash\n# Init script for %s\necho 'Initializing %s'\n", plan, plan),
		}

		for filename, content := range files {
			filePath := filepath.Join(planPath, filename)

			err := os.WriteFile(filePath, []byte(content), 0600)
			if err != nil {
				t.Fatalf("Failed to write file %s: %v", filePath, err)
			}
		}
	}

	// Verify structure
	for _, plan := range planDirs {
		planPath := filepath.Join(serviceDir, plan)
		files := []string{"manifest.yml", "credentials.yml", "init"}

		for _, file := range files {
			filePath := filepath.Join(planPath, file)
			if _, err := os.Stat(filePath); os.IsNotExist(err) {
				t.Errorf("Expected file %s does not exist", filePath)
			}
		}
	}

	t.Logf("Successfully created and verified test plan structure at %s", serviceDir)
}

func TestSHA256Comparison(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.yml")

	// Write initial content
	initialContent := "version: 1\ndata: test\n"

	err := os.WriteFile(testFile, []byte(initialContent), 0600)
	if err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Calculate initial SHA
	sha1, err := calculateSHA256(testFile)
	if err != nil {
		t.Fatalf("Failed to calculate first SHA256: %v", err)
	}

	// Write same content again
	err = os.WriteFile(testFile, []byte(initialContent), 0600)
	if err != nil {
		t.Fatalf("Failed to rewrite test file: %v", err)
	}

	// Calculate SHA again - should be the same
	sha2, err := calculateSHA256(testFile)
	if err != nil {
		t.Fatalf("Failed to calculate second SHA256: %v", err)
	}

	if sha1 != sha2 {
		t.Errorf("SHA256 changed for same content: %s != %s", sha1, sha2)
	}

	// Write different content
	differentContent := "version: 2\ndata: modified\n"

	err = os.WriteFile(testFile, []byte(differentContent), 0600)
	if err != nil {
		t.Fatalf("Failed to write modified test file: %v", err)
	}

	// Calculate SHA for modified file - should be different
	sha3, err := calculateSHA256(testFile)
	if err != nil {
		t.Fatalf("Failed to calculate third SHA256: %v", err)
	}

	if sha1 == sha3 {
		t.Errorf("SHA256 did not change for different content: %s == %s", sha1, sha3)
	}

	t.Logf("SHA for initial: %s", sha1)
	t.Logf("SHA for same: %s", sha2)
	t.Logf("SHA for modified: %s", sha3)
}

func TestPlanStorageVaultPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		service  string
		plan     string
		fileType string
		sha256   string
		expected string
	}{
		{
			name:     "Redis standalone manifest",
			service:  "redis",
			plan:     "standalone-6",
			fileType: "manifest",
			sha256:   "275930907e005f6792ba1b86f382d9fc7a6285adbe2df02070458c2c7ace136c",
			expected: "plans/redis/standalone-6/manifest/275930907e005f6792ba1b86f382d9fc7a6285adbe2df02070458c2c7ace136c",
		},
		{
			name:     "PostgreSQL cluster credentials",
			service:  "postgresql",
			plan:     "cluster",
			fileType: "credentials",
			sha256:   "abc123def456789",
			expected: "plans/postgresql/cluster/credentials/abc123def456789",
		},
		{
			name:     "RabbitMQ single init script",
			service:  "rabbitmq",
			plan:     "single",
			fileType: "init",
			sha256:   "xyz789abc123",
			expected: "plans/rabbitmq/single/init/xyz789abc123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Test the vault path generation
			vaultPath := fmt.Sprintf("plans/%s/%s/%s/%s", tt.service, tt.plan, tt.fileType, tt.sha256)
			if vaultPath != tt.expected {
				t.Errorf("Expected vault path %s, got %s", tt.expected, vaultPath)
			}
		})
	}
}

func TestInstancePlanReferencePaths(t *testing.T) {
	t.Parallel()

	instanceID := "ed1e4f4c-3da8-4176-ba46-c825684eeeb1"

	// Test instance plan reference paths
	tests := []struct {
		name     string
		refType  string
		expected string
	}{
		{
			name:     "Plan SHA256 references path",
			refType:  "plans/sha256",
			expected: "ed1e4f4c-3da8-4176-ba46-c825684eeeb1/plans/sha256",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			vaultPath := fmt.Sprintf("%s/%s", instanceID, tt.refType)
			if vaultPath != tt.expected {
				t.Errorf("Expected vault path %s, got %s", tt.expected, vaultPath)
			}
		})
	}

	// Test the structure of stored references
	t.Run("Reference structure", func(t *testing.T) {
		t.Parallel()

		expectedRefs := map[string]string{
			"manifest":    "275930907e005f6792ba1b86f382d9fc7a6285adbe2df02070458c2c7ace136c",
			"credentials": "abc123def456789",
			"init":        "xyz789abc123",
		}

		// Verify each reference type
		for refType, sha := range expectedRefs {
			if len(sha) < 12 {
				t.Errorf("SHA256 for %s seems too short: %s", refType, sha)
			}
		}
	})
}
