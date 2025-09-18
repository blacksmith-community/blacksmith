package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/vault/api"
)

// Static errors for err113 compliance.
var (
	ErrVaultClientNotConfigured = errors.New("vault client not configured")
	ErrVaultNotInitialized      = errors.New("vault not initialized")
	ErrVaultIsSealed            = errors.New("vault is sealed")
)

// PluginsAuditService provides audit logging functionality for rabbitmq-plugins commands.
type PluginsAuditService struct {
	vaultClient *api.Client
	logger      Logger
}

// PluginsAuditEntry represents an audit log entry for a rabbitmq-plugins command execution.
type PluginsAuditEntry struct {
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

// PluginsAuditSummary provides a summary of audit entries.
type PluginsAuditSummary struct {
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

// PluginsHistoryEntry represents a command execution history entry.
type PluginsHistoryEntry struct {
	Timestamp    int64    `json:"timestamp"`
	Category     string   `json:"category"`
	Command      string   `json:"command"`
	Arguments    []string `json:"arguments"`
	Success      bool     `json:"success"`
	Duration     int64    `json:"duration_ms"`
	User         string   `json:"user"`
	ExecutionID  string   `json:"execution_id"`
	OutputSample string   `json:"output_sample"`
}

// NewPluginsAuditService creates a new plugins audit service.
func NewPluginsAuditService(vaultClient *api.Client, logger Logger) *PluginsAuditService {
	if logger == nil {
		logger = &noOpLogger{}
	}

	return &PluginsAuditService{
		vaultClient: vaultClient,
		logger:      logger,
	}
}

// LogExecution logs a rabbitmq-plugins command execution to Vault.
func (a *PluginsAuditService) LogExecution(ctx context.Context, execution *RabbitMQPluginsExecution, user, clientIP, executionID string, duration int64) error {
	if a.vaultClient == nil {
		a.logger.Errorf("Vault client not configured, skipping audit logging")

		return ErrVaultClientNotConfigured
	}

	entry := a.createAuditEntry(execution, user, clientIP, executionID, duration)

	vaultPath := a.generateVaultPath(execution.InstanceID)

	entryData, err := a.entryToMap(entry)
	if err != nil {
		a.logger.Errorf("Failed to convert audit entry to map: %v", err)

		return fmt.Errorf("failed to convert audit entry: %w", err)
	}

	dataMap, err := a.getExistingAuditData(vaultPath)
	if err != nil {
		return err
	}

	timestampKey := strconv.FormatInt(execution.Timestamp, 10)
	dataMap[timestampKey] = entryData

	err = a.storeAuditEntry(vaultPath, dataMap)
	if err != nil {
		return err
	}

	a.logger.Infof("Successfully logged rabbitmq-plugins audit entry for instance %s, command %s with key %s",
		execution.InstanceID, execution.Command, timestampKey)

	return nil
}

// GetHistory retrieves command execution history for an instance.
func (a *PluginsAuditService) GetHistory(ctx context.Context, instanceID string, limit int) ([]PluginsHistoryEntry, error) {
	if a.vaultClient == nil {
		return nil, ErrVaultClientNotConfigured
	}

	dataMap, err := a.readAuditData(instanceID)
	if err != nil {
		return nil, err
	}

	if len(dataMap) == 0 {
		return []PluginsHistoryEntry{}, nil
	}

	timestampEntries := a.parseTimestampEntries(dataMap)

	// Sort by timestamp (most recent first)
	sort.Slice(timestampEntries, func(i, j int) bool {
		return timestampEntries[i].timestamp > timestampEntries[j].timestamp
	})

	return a.convertToHistoryEntries(timestampEntries, limit), nil
}

type timestampEntry struct {
	timestamp int64
	data      map[string]interface{}
}

// GetAuditSummary retrieves audit summary statistics for an instance.
func (a *PluginsAuditService) GetAuditSummary(ctx context.Context, instanceID string) (*PluginsAuditSummary, error) {
	history, err := a.GetHistory(ctx, instanceID, 0) // Get all entries for summary
	if err != nil {
		return nil, err
	}

	summary := &PluginsAuditSummary{
		Categories: make(map[string]int),
		Commands:   make(map[string]int),
		Users:      make(map[string]int),
		TimeRange:  make(map[string]int64),
	}

	if len(history) == 0 {
		return summary, nil
	}

	// Process entries
	summary.TotalEntries = len(history)
	summary.LastExecution = history[0].Timestamp               // First entry is most recent
	summary.FirstExecution = history[len(history)-1].Timestamp // Last entry is oldest

	for _, entry := range history {
		// Count by success/failure
		if entry.Success {
			summary.SuccessfulCmds++
		} else {
			summary.FailedCmds++
		}

		// Count by category
		summary.Categories[entry.Category]++

		// Count by command
		summary.Commands[entry.Command]++

		// Count by user
		summary.Users[entry.User]++
	}

	// Set time range
	summary.TimeRange["first"] = summary.FirstExecution
	summary.TimeRange["last"] = summary.LastExecution

	return summary, nil
}

// ClearHistory clears command execution history for an instance.
func (a *PluginsAuditService) ClearHistory(ctx context.Context, instanceID string) error {
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

	a.logger.Infof("Cleared all rabbitmq-plugins audit entries for instance %s", instanceID)

	return nil
}

// IsHealthy checks if the audit service is healthy.
func (a *PluginsAuditService) IsHealthy(ctx context.Context) error {
	if a.vaultClient == nil {
		return ErrVaultClientNotConfigured
	}

	// Try to read Vault health
	health, err := a.vaultClient.Sys().Health()
	if err != nil {
		return fmt.Errorf("vault health check failed: %w", err)
	}

	if !health.Initialized {
		return ErrVaultNotInitialized
	}

	if health.Sealed {
		return ErrVaultIsSealed
	}

	return nil
}

// GetPluginOperationHistory retrieves history filtered by plugin operations.
func (a *PluginsAuditService) GetPluginOperationHistory(ctx context.Context, instanceID, operation string, limit int) ([]PluginsHistoryEntry, error) {
	allHistory, err := a.GetHistory(ctx, instanceID, 0)
	if err != nil {
		return nil, err
	}

	var filteredHistory []PluginsHistoryEntry

	for _, entry := range allHistory {
		if operation == "" || entry.Command == operation {
			filteredHistory = append(filteredHistory, entry)
			if limit > 0 && len(filteredHistory) >= limit {
				break
			}
		}
	}

	return filteredHistory, nil
}

// GetRecentActivity retrieves recent plugin activity.
func (a *PluginsAuditService) GetRecentActivity(ctx context.Context, instanceID string, hours int) ([]PluginsHistoryEntry, error) {
	allHistory, err := a.GetHistory(ctx, instanceID, DefaultHistoryLimit) // Get recent entries
	if err != nil {
		return nil, err
	}

	cutoffTime := time.Now().Add(-time.Duration(hours)*time.Hour).Unix() * int64(MillisecondsPerSecond) // Convert to milliseconds

	var recentHistory []PluginsHistoryEntry

	for _, entry := range allHistory {
		if entry.Timestamp >= cutoffTime {
			recentHistory = append(recentHistory, entry)
		}
	}

	return recentHistory, nil
}

// FormatHistoryEntry formats a history entry for display.
func (a *PluginsAuditService) FormatHistoryEntry(entry *PluginsHistoryEntry) string {
	timestamp := time.Unix(entry.Timestamp/int64(MillisecondsPerSecond), 0).Format("2006-01-02 15:04:05")

	status := "✅"
	if !entry.Success {
		status = "❌"
	}

	args := ""
	if len(entry.Arguments) > 0 {
		args = " " + strings.Join(entry.Arguments, " ")
	}

	duration := fmt.Sprintf("%.2fs", float64(entry.Duration)/MillisecondsPerSecond)

	return fmt.Sprintf("[%s] %s %s%s - %s (%s)",
		timestamp, status, entry.Command, args, entry.User, duration)
}

func (a *PluginsAuditService) createAuditEntry(execution *RabbitMQPluginsExecution, user, clientIP, executionID string, duration int64) *PluginsAuditEntry {
	entry := &PluginsAuditEntry{
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

func (a *PluginsAuditService) getExistingAuditData(vaultPath string) (map[string]interface{}, error) {
	existingSecret, err := a.vaultClient.Logical().Read(vaultPath)
	if err != nil {
		a.logger.Errorf("Failed to read existing audit data from Vault: %v", err)

		return nil, fmt.Errorf("failed to read existing audit data: %w", err)
	}

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

	return dataMap, nil
}

func (a *PluginsAuditService) storeAuditEntry(vaultPath string, dataMap map[string]interface{}) error {
	// Store in Vault (KV v2 format requires data wrapper)
	vaultData := map[string]interface{}{
		"data": dataMap,
	}

	// Write to Vault
	_, err := a.vaultClient.Logical().Write(vaultPath, vaultData)
	if err != nil {
		a.logger.Errorf("Failed to write audit entry to Vault at %s: %v", vaultPath, err)

		return fmt.Errorf("failed to write to Vault: %w", err)
	}

	return nil
}

func (a *PluginsAuditService) readAuditData(instanceID string) (map[string]interface{}, error) {
	vaultPath := a.generateVaultPath(instanceID)

	secret, err := a.vaultClient.Logical().Read(vaultPath)
	if err != nil {
		a.logger.Errorf("Failed to read audit entries from Vault: %v", err)

		return nil, fmt.Errorf("failed to read audit entries: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return map[string]interface{}{}, nil
	}

	// For KV v2, the actual data is nested under "data" field
	if data, ok := secret.Data["data"].(map[string]interface{}); ok {
		return data, nil
	}

	return map[string]interface{}{}, nil
}

func (a *PluginsAuditService) parseTimestampEntries(dataMap map[string]interface{}) []timestampEntry {
	var timestampEntries []timestampEntry

	for key, value := range dataMap {
		// Parse timestamp from key
		var timestamp int64

		_, err := fmt.Sscanf(key, "%d", &timestamp)
		if err != nil {
			a.logger.Errorf("Failed to parse timestamp from key %s: %v", key, err)

			continue
		}

		if entryData, ok := value.(map[string]interface{}); ok {
			timestampEntries = append(timestampEntries, timestampEntry{
				timestamp: timestamp,
				data:      entryData,
			})
		}
	}

	return timestampEntries
}

func (a *PluginsAuditService) convertToHistoryEntries(timestampEntries []timestampEntry, limit int) []PluginsHistoryEntry {
	var history []PluginsHistoryEntry

	for i, te := range timestampEntries {
		if limit > 0 && i >= limit {
			break
		}

		entry := a.mapToHistoryEntry(te.data)
		if entry != nil {
			history = append(history, *entry)
		}
	}

	return history
}

// generateVaultPath generates the Vault path for the audit endpoint (KV v2 format).
func (a *PluginsAuditService) generateVaultPath(instanceID string) string {
	return fmt.Sprintf("secret/data/%s/rabbitmq-plugins/audit", instanceID)
}

// entryToMap converts an audit entry to a map for Vault storage.
func (a *PluginsAuditService) entryToMap(entry *PluginsAuditEntry) (map[string]interface{}, error) {
	// Convert to JSON and back to map to handle nested structures
	jsonData, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal plugin audit entry to JSON: %w", err)
	}

	var result map[string]interface{}

	err = json.Unmarshal(jsonData, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal plugin audit data from JSON: %w", err)
	}

	return result, nil
}

// mapToHistoryEntry converts a map from Vault to a history entry.
func (a *PluginsAuditService) mapToHistoryEntry(data map[string]interface{}) *PluginsHistoryEntry {
	entry := &PluginsHistoryEntry{}

	// Extract fields with type checking
	if timestamp, ok := data["timestamp"].(json.Number); ok {
		ts, err := timestamp.Int64()
		if err == nil {
			entry.Timestamp = ts
		}
	}

	if category, ok := data["category"].(string); ok {
		entry.Category = category
	}

	if command, ok := data["command"].(string); ok {
		entry.Command = command
	}

	if args, ok := data["arguments"].([]interface{}); ok {
		for _, arg := range args {
			if argStr, ok := arg.(string); ok {
				entry.Arguments = append(entry.Arguments, argStr)
			}
		}
	}

	if success, ok := data["success"].(bool); ok {
		entry.Success = success
	}

	if duration, ok := data["duration_ms"].(json.Number); ok {
		d, err := duration.Int64()
		if err == nil {
			entry.Duration = d
		}
	}

	if user, ok := data["user"].(string); ok {
		entry.User = user
	}

	if executionID, ok := data["execution_id"].(string); ok {
		entry.ExecutionID = executionID
	}

	if output, ok := data["output"].(string); ok {
		// Truncate output for history display (first 200 characters)
		if len(output) > MaxOutputLength {
			entry.OutputSample = output[:MaxOutputLength] + "..."
		} else {
			entry.OutputSample = output
		}
	}

	return entry
}
