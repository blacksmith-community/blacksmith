package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	"github.com/hashicorp/vault/api"
)

// AuditService provides audit logging functionality for rabbitmqctl commands.
type AuditService struct {
	vaultClient *api.Client
	logger      Logger
}

// AuditEntry represents an audit log entry for a rabbitmqctl command execution.
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

// AuditSummary provides a summary of audit entries.
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

// NewAuditService creates a new audit service.
func NewAuditService(vaultClient *api.Client, logger Logger) *AuditService {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &AuditService{
		vaultClient: vaultClient,
		logger:      logger,
	}
}

// LogExecution logs a rabbitmqctl command execution to Vault.
func (a *AuditService) LogExecution(ctx context.Context, execution *RabbitMQCtlExecution, user, clientIP, executionID string, duration int64) error {
	if a.vaultClient == nil {
		a.logger.Errorf("Vault client not configured, skipping audit logging")

		return ErrVaultClientNotConfigured
	}

	// Create audit entry
	entry := a.createAuditEntry(execution, user, clientIP, executionID, duration)

	// Generate Vault path for the audit endpoint
	vaultPath := a.generateVaultPath(execution.InstanceID)

	// Store the audit entry in Vault
	err := a.storeAuditEntry(vaultPath, entry, execution.Timestamp)
	if err != nil {
		return err
	}

	a.logger.Infof("Audit entry logged to Vault: %s with key %d", vaultPath, execution.Timestamp)

	return nil
}

// LogStreamingExecution logs a streaming command execution to Vault.
func (a *AuditService) LogStreamingExecution(ctx context.Context, result *StreamingExecutionResult, user, clientIP string) error {
	if a.vaultClient == nil {
		a.logger.Errorf("Vault client not configured, skipping audit logging")

		return ErrVaultClientNotConfigured
	}

	// Build the audit entry
	entry := a.buildStreamingAuditEntry(result, user, clientIP)

	// Generate Vault path for the audit endpoint
	vaultPath := a.generateVaultPath(result.InstanceID)

	// Store the audit entry in Vault
	err := a.storeAuditEntry(vaultPath, entry, result.StartTime.Unix())
	if err != nil {
		return err
	}

	a.logger.Infof("Streaming audit entry logged to Vault: %s with key %d", vaultPath, result.StartTime.Unix())

	return nil
}

// rabbitmqctlTimestampEntry represents an audit entry with its timestamp.
type rabbitmqctlTimestampEntry struct {
	timestamp int64
	data      map[string]interface{}
}

// GetAuditHistory retrieves audit history for an instance.
func (a *AuditService) GetAuditHistory(ctx context.Context, instanceID string, limit int) ([]AuditEntry, error) {
	if a.vaultClient == nil {
		return nil, ErrVaultClientNotConfigured
	}

	// Read the audit endpoint directly
	vaultPath := a.generateVaultPath(instanceID)

	secret, err := a.vaultClient.Logical().Read(vaultPath)
	if err != nil {
		a.logger.Errorf("Failed to read audit entries from Vault: %v", err)

		return nil, fmt.Errorf("failed to read audit entries: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return []AuditEntry{}, nil
	}

	// Extract and process the audit data
	dataMap := a.prepareDataMap(secret)
	if len(dataMap) == 0 {
		return []AuditEntry{}, nil
	}

	// Parse and sort entries
	timestampEntries := a.parseTimestampEntries(dataMap)
	a.sortTimestampEntries(timestampEntries)

	// Convert to audit entries with limit
	entries := a.convertToAuditEntries(timestampEntries, limit)
	a.logger.Infof("Retrieved %d audit entries for instance %s", len(entries), instanceID)

	return entries, nil
}

// parseTimestampEntries parses entries from the data map.

// GetAuditSummary generates a summary of audit entries for an instance.
func (a *AuditService) GetAuditSummary(ctx context.Context, instanceID string) (*AuditSummary, error) {
	// Get all audit entries (no limit for summary)
	entries, err := a.GetAuditHistory(ctx, instanceID, MaxAuditHistoryLimit)
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

// ClearAuditHistory clears audit history for an instance (dangerous operation).
func (a *AuditService) ClearAuditHistory(ctx context.Context, instanceID string) error {
	if a.vaultClient == nil {
		return ErrVaultClientNotConfigured
	}

	// Delete the entire audit endpoint
	vaultPath := a.generateVaultPath(instanceID)

	_, err := a.vaultClient.Logical().Delete(vaultPath)
	if err != nil {
		a.logger.Errorf("Failed to delete audit data at %s: %v", vaultPath, err)

		return fmt.Errorf("failed to clear audit history: %w", err)
	}

	a.logger.Infof("Cleared all audit entries for instance %s", instanceID)

	return nil
}

// HealthCheck verifies the audit service can connect to Vault.
func (a *AuditService) HealthCheck(ctx context.Context) error {
	if a.vaultClient == nil {
		return ErrVaultClientNotConfigured
	}

	// Try to read from Vault metadata root to verify connectivity (KV v2)
	_, err := a.vaultClient.Logical().Read("secret/metadata/")
	if err != nil {
		return fmt.Errorf("vault health check failed: %w", err)
	}

	return nil
}

func (a *AuditService) createAuditEntry(execution *RabbitMQCtlExecution, user, clientIP, executionID string, duration int64) *AuditEntry {
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

	return entry
}

func (a *AuditService) storeAuditEntry(vaultPath string, entry *AuditEntry, timestamp int64) error {
	// Convert entry to map for Vault storage
	entryData, err := a.entryToMap(entry)
	if err != nil {
		a.logger.Errorf("Failed to convert audit entry to map: %v", err)

		return fmt.Errorf("failed to convert audit entry: %w", err)
	}

	// First, read existing audit data
	existingSecret, err := a.vaultClient.Logical().Read(vaultPath)
	if err != nil {
		a.logger.Errorf("Failed to read existing audit data from Vault: %v", err)

		return fmt.Errorf("failed to read existing audit data: %w", err)
	}

	// Prepare the data map
	dataMap := a.prepareDataMap(existingSecret)

	// Add the new entry with timestamp as key
	timestampKey := strconv.FormatInt(timestamp, 10)
	dataMap[timestampKey] = entryData

	// Store in Vault (KV v2 format requires data wrapper)
	vaultData := map[string]interface{}{
		"data": dataMap,
	}

	_, err = a.vaultClient.Logical().Write(vaultPath, vaultData)
	if err != nil {
		a.logger.Errorf("Failed to write audit entry to Vault at %s: %v", vaultPath, err)

		return fmt.Errorf("failed to write to vault: %w", err)
	}

	return nil
}

func (a *AuditService) prepareDataMap(existingSecret *api.Secret) map[string]interface{} {
	if existingSecret != nil && existingSecret.Data != nil {
		// For KV v2, the actual data is nested under "data" field
		if data, ok := existingSecret.Data["data"].(map[string]interface{}); ok {
			return data
		}
	}

	return make(map[string]interface{})
}

func (a *AuditService) buildStreamingAuditEntry(result *StreamingExecutionResult, user, clientIP string) *AuditEntry {
	// Calculate duration
	var duration int64
	if result.EndTime != nil {
		duration = result.EndTime.Sub(result.StartTime).Milliseconds()
	}

	// Collect output from channel
	output := a.collectStreamingOutput(result.Output)

	return &AuditEntry{
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
}

func (a *AuditService) collectStreamingOutput(outputChan chan string) string {
	var outputLines []string

	outputDone := false

	for !outputDone {
		select {
		case line, ok := <-outputChan:
			if !ok {
				outputDone = true
			} else {
				outputLines = append(outputLines, line)
			}
		default:
			outputDone = true
		}
	}

	if len(outputLines) > 0 {
		return fmt.Sprintf("%s... [%d lines total]", outputLines[0], len(outputLines))
	}

	return ""
}

func (a *AuditService) parseTimestampEntries(dataMap map[string]interface{}) []rabbitmqctlTimestampEntry {
	var entries []rabbitmqctlTimestampEntry

	for timestampStr, entryData := range dataMap {
		timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
		if err != nil {
			a.logger.Errorf("Failed to parse timestamp %s: %v", timestampStr, err)

			continue
		}

		if entryMap, ok := entryData.(map[string]interface{}); ok {
			entries = append(entries, rabbitmqctlTimestampEntry{
				timestamp: timestamp,
				data:      entryMap,
			})
		}
	}

	return entries
}

func (a *AuditService) sortTimestampEntries(entries []rabbitmqctlTimestampEntry) {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].timestamp > entries[j].timestamp
	})
}

func (a *AuditService) convertToAuditEntries(timestampEntries []rabbitmqctlTimestampEntry, limit int) []AuditEntry {
	// Pre-allocate slice based on limit or length of entries
	capacity := len(timestampEntries)
	if limit > 0 && limit < capacity {
		capacity = limit
	}

	auditEntries := make([]AuditEntry, 0, capacity)

	count := 0
	for _, timestampEntry := range timestampEntries {
		if limit > 0 && count >= limit {
			break
		}

		// Convert entry data back to AuditEntry
		entryBytes, err := json.Marshal(timestampEntry.data)
		if err != nil {
			a.logger.Errorf("Failed to marshal entry data: %v", err)

			continue
		}

		var entry AuditEntry

		err = json.Unmarshal(entryBytes, &entry)
		if err != nil {
			a.logger.Errorf("Failed to unmarshal audit entry: %v", err)

			continue
		}

		auditEntries = append(auditEntries, entry)
		count++
	}

	return auditEntries
}

// generateVaultPath generates the Vault path for the audit endpoint (KV v2 format).
func (a *AuditService) generateVaultPath(instanceID string) string {
	// Format for KV v2: secret/data/{instance-id}/rabbitmqctl/audit
	return fmt.Sprintf("secret/data/%s/rabbitmqctl/audit", instanceID)
}

// entryToMap converts an AuditEntry to a map for Vault storage.
func (a *AuditService) entryToMap(entry *AuditEntry) (map[string]interface{}, error) {
	entryJSON, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal audit entry: %w", err)
	}

	var entryMap map[string]interface{}

	err = json.Unmarshal(entryJSON, &entryMap)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal entry to map: %w", err)
	}

	return entryMap, nil
}
