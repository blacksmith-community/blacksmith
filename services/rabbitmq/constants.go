package rabbitmq

// Timeout Constants.
const (
	DefaultPluginTimeout      = 30
	LongPluginTimeout         = 60
	ExtendedPluginTimeout     = 120
	DefaultRabbitmqctlTimeout = 10
	LongRabbitmqctlTimeout    = 60
)

// Audit and History Constants.
const (
	MaxOutputLength       = 200
	DefaultHistoryLimit   = 100
	MaxAuditHistoryLimit  = 10000
	MillisecondsPerSecond = 1000.0
)

// Channel and Buffer Sizes.
const (
	OutputChannelBufferSize = 100
)

// SSH Service Constants.
const (
	MaxSSHOutputSize  = 1024 * 1024 // 1MB
	MinFieldsForQueue = 4
	TabSeparatorParts = 2
)

// Parsing Constants.
const (
	ErrorOutputParts = 2
	PluginMatchParts = 3
)
