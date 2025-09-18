package compression

// Config defines HTTP compression settings.
type Config struct {
	Enabled      bool     `yaml:"enabled"`       // Whether compression is enabled (default: true)
	Types        []string `yaml:"types"`         // Compression types to support: gzip, deflate, brotli (default: ["gzip"])
	Level        int      `yaml:"level"`         // Compression level 1-9, -1 for default (default: -1)
	MinSize      int      `yaml:"min_size"`      // Minimum response size to compress in bytes (default: 1024)
	ContentTypes []string `yaml:"content_types"` // MIME types to compress (default: text/*, application/json, etc.)
}
