package websocket

import "time"

// Timeout and Interval Constants.
const (
	SSHDialTimeout   = 10 * time.Second
	HandshakeTimeout = 10 * time.Second
	PingInterval     = 30 * time.Second
	PongTimeout      = 10 * time.Second
	SessionTimeout   = 30 * time.Minute
	ShortSleep       = 10 * time.Millisecond
	MediumSleep      = 50 * time.Millisecond
	LongSleep        = 100 * time.Millisecond
)

// Message Size Constants.
const (
	MaxMessageSize = 32 * 1024 // 32KB
)

// Terminal Size Constants.
const (
	DefaultTerminalWidth  = 80
	DefaultTerminalHeight = 24
)
