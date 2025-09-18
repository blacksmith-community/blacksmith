package logger

// MigrationHelpers provides utilities to help migrate from StandardLog to Logger interface
// These will be removed after migration is complete

// ExtractContext extracts context strings from a StandardLog for migration.
func ExtractContext(standard *StandardLog) []string {
	if standard == nil {
		return nil
	}

	return standard.ctx
}

// CreateNamedLogger creates a named logger from context strings.
func CreateNamedLogger(contextParts []string) Logger {
	baseLogger := Get()

	// If no context, return base logger
	if len(contextParts) == 0 {
		return baseLogger
	}

	// Join context parts with "." separator for hierarchical naming
	result := baseLogger

	for _, part := range contextParts {
		if part != "" && part != "*" {
			result = result.Named(part)
		}
	}

	return result
}

// MigrateFromStandard converts a StandardLog to a Logger interface
// This preserves the context hierarchy established by Wrap() calls.
func MigrateFromStandard(standard *StandardLog) Logger {
	if standard == nil {
		return Get()
	}

	context := ExtractContext(standard)

	return CreateNamedLogger(context)
}

// WrapToNamed converts old Wrap pattern to Named pattern.
func WrapToNamed(logger Logger, name string) Logger {
	return logger.Named(name)
}

// AuditLog creates structured audit logging compatible with old Audit() method.
func AuditLog(logger Logger, message string, args ...interface{}) {
	logger.WithField("audit", true).Infof(message, args...)
}
