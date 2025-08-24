package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/hashicorp/vault/api"
)

// AuditService provides audit logging functionality for rabbitmqctl commands
type AuditService struct {
	vaultClient *api.Client
	logger      Logger
}

// AuditEntry represents an audit log entry for a rabbitmqctl command execution
type AuditEntry struct {
	Timestamp   int64                  `json:"timestamp"`
	InstanceID  string                 `json:"instance_id"`
	User        string                 `json:"user"`
	ClientIP    string                 `json:"client_ip"`
	Category    string                 `json:"category"`
	Command     string                 `json:"command"`
	Arguments   []string               `json:"arguments"`
	Success     bool                   `json:"success"`
	ExitCode    int                    `json:"exit_code"`
	Duration    int64                  `json:"duration_ms"`
	Output      string                 `json:"output"`
	Error       string                 `json:"error,omitempty"`
	ExecutionID string                 `json:"execution_id"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// AuditSummary provides a summary of audit entries
type AuditSummary struct {
	TotalEntries   int              `json:"total_entries"`
	SuccessfulCmds int              `json:"successful_commands"`
	FailedCmds     int              `json:"failed_commands"`
	Categories     map[string]int   `json:"categories"`
	Commands       map[string]int   `json:"commands"`
	Users          map[string]int   `json:"users"`
	TimeRange      map[string]int64 `json:"time_range"`
	LastExecution  int64            `json:"last_execution"`
	FirstExecution int64            `json:"first_execution"`
}

// NewAuditService creates a new audit service
func NewAuditService(vaultClient *api.Client, logger Logger) *AuditService {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &AuditService{
		vaultClient: vaultClient,
		logger:      logger,
	}
}

// LogExecution logs a rabbitmqctl command execution to Vault
func (a *AuditService) LogExecution(ctx context.Context, execution *RabbitMQCtlExecution, user, clientIP, executionID string, duration int64) error {
	if a.vaultClient == nil {
		a.logger.Error("Vault client not configured, skipping audit logging")
		return fmt.Errorf("vault client not configured")
	}

	// Create audit entry
	entry := &AuditEntry{
		Timestamp:   execution.Timestamp,
		InstanceID:  execution.InstanceID,
		User:        user,
		ClientIP:    clientIP,
		Category:    execution.Category,
		Command:     execution.Command,
		Arguments:   execution.Arguments,
		Success:     execution.Success,
		ExitCode:    execution.ExitCode,
		Duration:    duration,
		Output:      execution.Output,
		ExecutionID: executionID,
	}

	if !execution.Success {
		entry.Error = fmt.Sprintf("Command failed with exit code %d", execution.ExitCode)
	}

	// Generate Vault path for the audit endpoint
	vaultPath := a.generateVaultPath(execution.InstanceID)

	// Convert entry to map for Vault storage
	entryData, err := a.entryToMap(entry)
	if err != nil {
		a.logger.Error("Failed to convert audit entry to map: %v", err)
		return fmt.Errorf("failed to convert audit entry: %v", err)
	}

	// First, read existing audit data
	existingSecret, err := a.vaultClient.Logical().Read(vaultPath)
	if err != nil {
		a.logger.Error("Failed to read existing audit data from Vault: %v", err)
		return fmt.Errorf("failed to read existing audit data: %v", err)
	}

	// Prepare the data map
	var dataMap map[string]interface{}
	if existingSecret != nil && existingSecret.Data != nil {
		// For KV v2, the actual data is nested under "data" field
		if data, ok := existingSecret.Data["data"].(map[string]interface{}); ok {
			dataMap = data
		} else {
			dataMap = make(map[string]interface{})
		}
	} else {
		dataMap = make(map[string]interface{})
	}

	// Add the new entry with timestamp as key
	timestampKey := fmt.Sprintf("%d", execution.Timestamp)
	dataMap[timestampKey] = entryData

	// Store in Vault (KV v2 format requires data wrapper)
	vaultData := map[string]interface{}{
		"data": dataMap,
	}
	_, err = a.vaultClient.Logical().Write(vaultPath, vaultData)
	if err != nil {
		a.logger.Error("Failed to write audit entry to Vault at %s: %v", vaultPath, err)
		return fmt.Errorf("failed to write to vault: %v", err)
	}

	a.logger.Info("Audit entry logged to Vault: %s with key %s", vaultPath, timestampKey)
	return nil
}

// LogStreamingExecution logs a streaming command execution to Vault
func (a *AuditService) LogStreamingExecution(ctx context.Context, result *StreamingExecutionResult, user, clientIP string) error {
	if a.vaultClient == nil {
		a.logger.Error("Vault client not configured, skipping audit logging")
		return fmt.Errorf("vault client not configured")
	}

	// Calculate duration
	var duration int64
	if result.EndTime != nil {
		duration = result.EndTime.Sub(result.StartTime).Milliseconds()
	}

	// Collect output from channel (non-blocking)
	var outputLines []string
	outputDone := false
	for !outputDone {
		select {
		case line, ok := <-result.Output:
			if !ok {
				outputDone = true
			} else {
				outputLines = append(outputLines, line)
			}
		default:
			outputDone = true
		}
	}
	output := ""
	if len(outputLines) > 0 {
		output = fmt.Sprintf("%s... [%d lines total]", outputLines[0], len(outputLines))
	}

	// Create audit entry
	entry := &AuditEntry{
		Timestamp:   result.StartTime.Unix(),
		InstanceID:  result.InstanceID,
		User:        user,
		ClientIP:    clientIP,
		Category:    result.Category,
		Command:     result.Command,
		Arguments:   result.Arguments,
		Success:     result.Success,
		ExitCode:    result.ExitCode,
		Duration:    duration,
		Output:      output,
		Error:       result.Error,
		ExecutionID: result.ExecutionID,
		Metadata:    result.Metadata,
	}

	// Generate Vault path for the audit endpoint
	vaultPath := a.generateVaultPath(result.InstanceID)

	// Convert entry to map for Vault storage
	entryData, err := a.entryToMap(entry)
	if err != nil {
		a.logger.Error("Failed to convert streaming audit entry to map: %v", err)
		return fmt.Errorf("failed to convert audit entry: %v", err)
	}

	// First, read existing audit data
	existingSecret, err := a.vaultClient.Logical().Read(vaultPath)
	if err != nil {
		a.logger.Error("Failed to read existing audit data from Vault: %v", err)
		return fmt.Errorf("failed to read existing audit data: %v", err)
	}

	// Prepare the data map
	var dataMap map[string]interface{}
	if existingSecret != nil && existingSecret.Data != nil {
		// For KV v2, the actual data is nested under "data" field
		if data, ok := existingSecret.Data["data"].(map[string]interface{}); ok {
			dataMap = data
		} else {
			dataMap = make(map[string]interface{})
		}
	} else {
		dataMap = make(map[string]interface{})
	}

	// Add the new entry with timestamp as key
	timestampKey := fmt.Sprintf("%d", result.StartTime.Unix())
	dataMap[timestampKey] = entryData

	// Store in Vault (KV v2 format requires data wrapper)
	vaultData := map[string]interface{}{
		"data": dataMap,
	}
	_, err = a.vaultClient.Logical().Write(vaultPath, vaultData)
	if err != nil {
		a.logger.Error("Failed to write streaming audit entry to Vault at %s: %v", vaultPath, err)
		return fmt.Errorf("failed to write to vault: %v", err)
	}

	a.logger.Info("Streaming audit entry logged to Vault: %s with key %s", vaultPath, timestampKey)
	return nil
}

// GetAuditHistory retrieves audit history for an instance
func (a *AuditService) GetAuditHistory(ctx context.Context, instanceID string, limit int) ([]AuditEntry, error) {
	if a.vaultClient == nil {
		return nil, fmt.Errorf("vault client not configured")
	}

	// Read the audit endpoint directly
	vaultPath := a.generateVaultPath(instanceID)
	secret, err := a.vaultClient.Logical().Read(vaultPath)
	if err != nil {
		a.logger.Error("Failed to read audit entries from Vault: %v", err)
		return nil, fmt.Errorf("failed to read audit entries: %v", err)
	}

	if secret == nil || secret.Data == nil {
		return []AuditEntry{}, nil
	}

	// For KV v2, the actual data is nested under "data" field
	var dataMap map[string]interface{}
	if data, ok := secret.Data["data"].(map[string]interface{}); ok {
		dataMap = data
	} else {
		return []AuditEntry{}, nil
	}

	// Convert timestamp keys to integers for sorting
	type timestampEntry struct {
		timestamp int64
		data      map[string]interface{}
	}
	var timestampEntries []timestampEntry

	for key, value := range dataMap {
		// Parse timestamp from key
		var timestamp int64
		if _, err := fmt.Sscanf(key, "%d", &timestamp); err != nil {
			a.logger.Error("Failed to parse timestamp from key %s: %v", key, err)
			continue
		}

		if entryData, ok := value.(map[string]interface{}); ok {
			timestampEntries = append(timestampEntries, timestampEntry{
				timestamp: timestamp,
				data:      entryData,
			})
		}
	}

	// Sort by timestamp (most recent first)
	sort.Slice(timestampEntries, func(i, j int) bool {
		return timestampEntries[i].timestamp > timestampEntries[j].timestamp
	})

	// Convert to audit entries (limit the results)
	var entries []AuditEntry
	for i, te := range timestampEntries {
		if i >= limit {
			break
		}

		entry, err := a.mapToEntry(te.data)
		if err != nil {
			a.logger.Error("Failed to parse audit entry for timestamp %d: %v", te.timestamp, err)
			continue
		}

		entries = append(entries, *entry)
	}

	a.logger.Info("Retrieved %d audit entries for instance %s", len(entries), instanceID)
	return entries, nil
}

// GetAuditSummary generates a summary of audit entries for an instance
func (a *AuditService) GetAuditSummary(ctx context.Context, instanceID string) (*AuditSummary, error) {
	// Get all audit entries (no limit for summary)
	entries, err := a.GetAuditHistory(ctx, instanceID, 10000)
	if err != nil {
		return nil, err
	}

	if len(entries) == 0 {
		return &AuditSummary{
			Categories: make(map[string]int),
			Commands:   make(map[string]int),
			Users:      make(map[string]int),
			TimeRange:  make(map[string]int64),
		}, nil
	}

	// Calculate summary
	summary := &AuditSummary{
		TotalEntries:   len(entries),
		Categories:     make(map[string]int),
		Commands:       make(map[string]int),
		Users:          make(map[string]int),
		TimeRange:      make(map[string]int64),
		FirstExecution: entries[len(entries)-1].Timestamp, // Oldest (entries are newest first)
		LastExecution:  entries[0].Timestamp,              // Newest
	}

	for _, entry := range entries {
		// Count success/failure
		if entry.Success {
			summary.SuccessfulCmds++
		} else {
			summary.FailedCmds++
		}

		// Count by category
		summary.Categories[entry.Category]++

		// Count by command
		commandKey := fmt.Sprintf("%s.%s", entry.Category, entry.Command)
		summary.Commands[commandKey]++

		// Count by user
		summary.Users[entry.User]++
	}

	return summary, nil
}

// ClearAuditHistory clears audit history for an instance (dangerous operation)
func (a *AuditService) ClearAuditHistory(ctx context.Context, instanceID string) error {
	if a.vaultClient == nil {
		return fmt.Errorf("vault client not configured")
	}

	// Delete the entire audit endpoint
	vaultPath := a.generateVaultPath(instanceID)
	_, err := a.vaultClient.Logical().Delete(vaultPath)
	if err != nil {
		a.logger.Error("Failed to delete audit data at %s: %v", vaultPath, err)
		return fmt.Errorf("failed to clear audit history: %v", err)
	}

	a.logger.Info("Cleared all audit entries for instance %s", instanceID)
	return nil
}

// generateVaultPath generates the Vault path for the audit endpoint (KV v2 format)
func (a *AuditService) generateVaultPath(instanceID string) string {
	// Format for KV v2: secret/data/{instance-id}/rabbitmqctl/audit
	return fmt.Sprintf("secret/data/%s/rabbitmqctl/audit", instanceID)
}

// entryToMap converts an AuditEntry to a map for Vault storage
func (a *AuditService) entryToMap(entry *AuditEntry) (map[string]interface{}, error) {
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}

	var entryMap map[string]interface{}
	err = json.Unmarshal(entryJSON, &entryMap)
	if err != nil {
		return nil, err
	}

	return entryMap, nil
}

// mapToEntry converts a map from Vault to an AuditEntry
func (a *AuditService) mapToEntry(data map[string]interface{}) (*AuditEntry, error) {
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	var entry AuditEntry
	err = json.Unmarshal(dataJSON, &entry)
	if err != nil {
		return nil, err
	}

	return &entry, nil
}

// HealthCheck verifies the audit service can connect to Vault
func (a *AuditService) HealthCheck(ctx context.Context) error {
	if a.vaultClient == nil {
		return fmt.Errorf("vault client not configured")
	}

	// Try to read from Vault metadata root to verify connectivity (KV v2)
	_, err := a.vaultClient.Logical().Read("secret/metadata/")
	if err != nil {
		return fmt.Errorf("vault health check failed: %v", err)
	}

	return nil
}
