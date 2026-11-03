// Package cache provides a high-performance caching system with support for
// sharded concurrent access, LRU eviction, and TTL-based expiration.
package cache

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// DiskCache implements a persistent disk-based cache (L2).
// It stores cache entries as files in a configurable directory using
// SHA-256 hashed filenames for safe storage.
type DiskCache struct {
	path    string
	maxSize int64 // Maximum size in bytes
	ttl     time.Duration

	// Options
	compression bool
	encoding    DiskEncoding

	// Statistics
	hits      atomic.Uint64
	misses    atomic.Uint64
	sets      atomic.Uint64
	evictions atomic.Uint64

	// Size tracking
	currentSize atomic.Int64
	entryCount  atomic.Int64

	// Index for tracking entries
	index   map[string]*diskEntryMeta
	indexMu sync.RWMutex

	// Background cleanup
	cleanupInterval time.Duration
	stopCleanup     chan struct{}
	cleanupDone     chan struct{}
}

// DiskEncoding specifies the serialization format for disk cache entries.
type DiskEncoding int

const (
	// EncodingGob uses Go's gob encoding (efficient for Go types).
	EncodingGob DiskEncoding = iota
	// EncodingJSON uses JSON encoding (human-readable, cross-language).
	EncodingJSON
)

// diskEntry represents a serialized cache entry on disk.
type diskEntry struct {
	Key       string      `json:"key"`
	Value     interface{} `json:"value"`
	CreatedAt time.Time   `json:"created_at"`
	ExpiresAt time.Time   `json:"expires_at,omitempty"`
	Size      int64       `json:"size"`
}

// diskEntryMeta tracks metadata about entries without loading values.
type diskEntryMeta struct {
	Key        string
	FilePath   string
	Size       int64
	CreatedAt  time.Time
	ExpiresAt  time.Time
	AccessedAt time.Time
}

// DiskCacheOptions holds configuration for the disk cache.
type DiskCacheOptions struct {
	// MaxSize is the maximum total size in bytes (0 means unlimited).
	MaxSize int64

	// TTL is the default time-to-live for entries (0 means no expiration).
	TTL time.Duration

	// Compression enables gzip compression for stored entries.
	Compression bool

	// Encoding specifies the serialization format (gob or JSON).
	Encoding DiskEncoding

	// CleanupInterval is how often expired entries are removed (0 disables).
	CleanupInterval time.Duration
}

// DiskOption is a functional option for configuring a DiskCache.
type DiskOption func(*DiskCacheOptions)

// DefaultDiskOptions returns sensible default options for disk cache.
func DefaultDiskOptions() DiskCacheOptions {
	return DiskCacheOptions{
		MaxSize:         1 << 30, // 1GB
		TTL:             time.Hour,
		Compression:     false,
		Encoding:        EncodingGob,
		CleanupInterval: 5 * time.Minute,
	}
}

// WithDiskMaxSize sets the maximum cache size in bytes.
func WithDiskMaxSize(size int64) DiskOption {
	return func(o *DiskCacheOptions) {
		if size > 0 {
			o.MaxSize = size
		}
	}
}

// WithDiskTTL sets the default time-to-live for entries.
func WithDiskTTL(ttl time.Duration) DiskOption {
	return func(o *DiskCacheOptions) {
		o.TTL = ttl
	}
}

// WithDiskCompression enables or disables gzip compression.
func WithDiskCompression(enabled bool) DiskOption {
	return func(o *DiskCacheOptions) {
		o.Compression = enabled
	}
}

// WithDiskEncoding sets the serialization format.
func WithDiskEncoding(encoding DiskEncoding) DiskOption {
	return func(o *DiskCacheOptions) {
		o.Encoding = encoding
	}
}

// WithDiskCleanupInterval sets how often expired entries are cleaned.
func WithDiskCleanupInterval(interval time.Duration) DiskOption {
	return func(o *DiskCacheOptions) {
		o.CleanupInterval = interval
	}
}

// NewDiskCache creates a new disk-based cache at the specified path.
// The directory will be created if it doesn't exist.
func NewDiskCache(path string, opts ...DiskOption) (*DiskCache, error) {
	options := DefaultDiskOptions()
	for _, opt := range opts {
		opt(&options)
	}

	// Create directory if needed
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	dc := &DiskCache{
		path:            path,
		maxSize:         options.MaxSize,
		ttl:             options.TTL,
		compression:     options.Compression,
		encoding:        options.Encoding,
		cleanupInterval: options.CleanupInterval,
		index:           make(map[string]*diskEntryMeta),
		stopCleanup:     make(chan struct{}),
		cleanupDone:     make(chan struct{}),
	}

	// Load existing entries into index
	if err := dc.loadIndex(); err != nil {
		return nil, fmt.Errorf("failed to load cache index: %w", err)
	}

	// Start background cleanup if interval is set
	if options.CleanupInterval > 0 {
		go dc.cleanupLoop()
	} else {
		close(dc.cleanupDone)
	}

	return dc, nil
}

// Get retrieves a value from the disk cache.
func (dc *DiskCache) Get(key string) (interface{}, bool) {
	dc.indexMu.RLock()
	meta, exists := dc.index[key]
	dc.indexMu.RUnlock()

	if !exists {
		dc.misses.Add(1)
		return nil, false
	}

	// Check expiration
	if !meta.ExpiresAt.IsZero() && time.Now().After(meta.ExpiresAt) {
		dc.Delete(key)
		dc.misses.Add(1)
		return nil, false
	}

	// Read entry from disk
	entry, err := dc.readEntry(meta.FilePath)
	if err != nil {
		dc.misses.Add(1)
		return nil, false
	}

	// Update access time
	dc.indexMu.Lock()
	if m, ok := dc.index[key]; ok {
		m.AccessedAt = time.Now()
	}
	dc.indexMu.Unlock()

	dc.hits.Add(1)
	return entry.Value, true
}

// Set adds or updates a value in the disk cache.
func (dc *DiskCache) Set(key string, value interface{}) {
	dc.SetWithTTL(key, value, dc.ttl)
}

// SetWithTTL adds or updates a value with a specific TTL.
func (dc *DiskCache) SetWithTTL(key string, value interface{}, ttl time.Duration) {
	now := time.Now()
	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = now.Add(ttl)
	}

	entry := &diskEntry{
		Key:       key,
		Value:     value,
		CreatedAt: now,
		ExpiresAt: expiresAt,
	}

	filePath := dc.keyToPath(key)

	// Write entry atomically
	size, err := dc.writeEntryAtomic(filePath, entry)
	if err != nil {
		return
	}

	entry.Size = size

	// Update index
	dc.indexMu.Lock()
	oldMeta, existed := dc.index[key]
	if existed {
		dc.currentSize.Add(-oldMeta.Size)
	} else {
		dc.entryCount.Add(1)
	}

	dc.index[key] = &diskEntryMeta{
		Key:        key,
		FilePath:   filePath,
		Size:       size,
		CreatedAt:  now,
		ExpiresAt:  expiresAt,
		AccessedAt: now,
	}
	dc.currentSize.Add(size)
	dc.indexMu.Unlock()

	dc.sets.Add(1)

	// Evict if over size limit
	dc.evictIfNeeded()
}

// Delete removes a value from the disk cache.
func (dc *DiskCache) Delete(key string) {
	dc.indexMu.Lock()
	meta, exists := dc.index[key]
	if exists {
		delete(dc.index, key)
		dc.currentSize.Add(-meta.Size)
		dc.entryCount.Add(-1)
	}
	dc.indexMu.Unlock()

	if exists {
		// Remove file (ignore errors)
		_ = os.Remove(meta.FilePath)
	}
}

// Clear removes all entries from the disk cache.
func (dc *DiskCache) Clear() {
	dc.indexMu.Lock()
	entries := make([]*diskEntryMeta, 0, len(dc.index))
	for _, meta := range dc.index {
		entries = append(entries, meta)
	}
	dc.index = make(map[string]*diskEntryMeta)
	dc.currentSize.Store(0)
	dc.entryCount.Store(0)
	dc.indexMu.Unlock()

	// Remove all files
	for _, meta := range entries {
		_ = os.Remove(meta.FilePath)
	}
}

// Size returns the number of entries in the cache.
func (dc *DiskCache) Size() int {
	return int(dc.entryCount.Load())
}

// SizeBytes returns the total size of cache entries in bytes.
func (dc *DiskCache) SizeBytes() int64 {
	return dc.currentSize.Load()
}

// Stats returns current cache statistics.
func (dc *DiskCache) Stats() Stats {
	return Stats{
		Hits:      dc.hits.Load(),
		Misses:    dc.misses.Load(),
		Size:      dc.Size(),
		Evictions: dc.evictions.Load(),
		Sets:      dc.sets.Load(),
	}
}

// DiskStats returns disk-specific statistics.
func (dc *DiskCache) DiskStats() DiskCacheStats {
	dc.indexMu.RLock()
	entryCount := len(dc.index)
	dc.indexMu.RUnlock()

	return DiskCacheStats{
		Hits:        dc.hits.Load(),
		Misses:      dc.misses.Load(),
		Sets:        dc.sets.Load(),
		Evictions:   dc.evictions.Load(),
		EntryCount:  entryCount,
		SizeBytes:   dc.currentSize.Load(),
		MaxSize:     dc.maxSize,
		Compression: dc.compression,
		Path:        dc.path,
	}
}

// DiskCacheStats holds disk cache specific statistics.
type DiskCacheStats struct {
	Hits        uint64
	Misses      uint64
	Sets        uint64
	Evictions   uint64
	EntryCount  int
	SizeBytes   int64
	MaxSize     int64
	Compression bool
	Path        string
}

// Close stops background goroutines and saves state.
func (dc *DiskCache) Close() error {
	select {
	case <-dc.stopCleanup:
		// Already closed
	default:
		close(dc.stopCleanup)
	}
	<-dc.cleanupDone
	return nil
}

// Contains checks if a key exists without loading the value.
func (dc *DiskCache) Contains(key string) bool {
	dc.indexMu.RLock()
	meta, exists := dc.index[key]
	dc.indexMu.RUnlock()

	if !exists {
		return false
	}

	// Check expiration
	if !meta.ExpiresAt.IsZero() && time.Now().After(meta.ExpiresAt) {
		return false
	}

	return true
}

// Keys returns all keys in the cache.
func (dc *DiskCache) Keys() []string {
	dc.indexMu.RLock()
	defer dc.indexMu.RUnlock()

	keys := make([]string, 0, len(dc.index))
	now := time.Now()
	for key, meta := range dc.index {
		// Skip expired entries
		if !meta.ExpiresAt.IsZero() && now.After(meta.ExpiresAt) {
			continue
		}
		keys = append(keys, key)
	}
	return keys
}

// keyToPath converts a cache key to a file path using SHA-256 hashing.
func (dc *DiskCache) keyToPath(key string) string {
	hash := sha256.Sum256([]byte(key))
	filename := hex.EncodeToString(hash[:])
	ext := ".gob"
	if dc.encoding == EncodingJSON {
		ext = ".json"
	}
	if dc.compression {
		ext += ".gz"
	}
	return filepath.Join(dc.path, filename+ext)
}

// writeEntryAtomic writes an entry to disk atomically using temp file + rename.
func (dc *DiskCache) writeEntryAtomic(path string, entry *diskEntry) (int64, error) {
	// Create temp file in same directory
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".cache-*")
	if err != nil {
		return 0, fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Cleanup on error
	success := false
	defer func() {
		if !success {
			_ = tmpFile.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	var writer io.Writer = tmpFile
	var gzWriter *gzip.Writer

	// Add compression if enabled
	if dc.compression {
		gzWriter = gzip.NewWriter(tmpFile)
		writer = gzWriter
	}

	// Encode entry
	var encodeErr error
	switch dc.encoding {
	case EncodingGob:
		encoder := gob.NewEncoder(writer)
		encodeErr = encoder.Encode(entry)
	case EncodingJSON:
		encoder := json.NewEncoder(writer)
		encodeErr = encoder.Encode(entry)
	}

	if encodeErr != nil {
		return 0, fmt.Errorf("failed to encode entry: %w", encodeErr)
	}

	// Close gzip writer to flush
	if gzWriter != nil {
		if gzErr := gzWriter.Close(); gzErr != nil {
			return 0, fmt.Errorf("failed to close gzip writer: %w", gzErr)
		}
	}

	// Get size before closing
	stat, err := tmpFile.Stat()
	if err != nil {
		return 0, fmt.Errorf("failed to stat temp file: %w", err)
	}
	size := stat.Size()

	// Close temp file
	if err := tmpFile.Close(); err != nil {
		return 0, fmt.Errorf("failed to close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, path); err != nil {
		return 0, fmt.Errorf("failed to rename temp file: %w", err)
	}

	success = true
	return size, nil
}

// readEntry reads and decodes a cache entry from disk.
func (dc *DiskCache) readEntry(path string) (*diskEntry, error) {
	// #nosec G304 - path is internally generated from SHA-256 hash of cache key
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer func() { _ = file.Close() }()

	var reader io.Reader = file

	// Add decompression if enabled
	if dc.compression {
		gzReader, err := gzip.NewReader(file)
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		defer func() { _ = gzReader.Close() }()
		reader = gzReader
	}

	// Decode entry
	var entry diskEntry
	switch dc.encoding {
	case EncodingGob:
		decoder := gob.NewDecoder(reader)
		if err := decoder.Decode(&entry); err != nil {
			return nil, fmt.Errorf("failed to decode gob: %w", err)
		}
	case EncodingJSON:
		decoder := json.NewDecoder(reader)
		if err := decoder.Decode(&entry); err != nil {
			return nil, fmt.Errorf("failed to decode json: %w", err)
		}
	}

	return &entry, nil
}

// loadIndex scans the cache directory and rebuilds the in-memory index.
func (dc *DiskCache) loadIndex() error {
	entries, err := os.ReadDir(dc.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	now := time.Now()
	var totalSize int64
	var count int64

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Skip temp files
		if name != "" && name[0] == '.' {
			continue
		}

		path := filepath.Join(dc.path, name)
		diskEntry, err := dc.readEntry(path)
		if err != nil {
			// Remove corrupted entry
			_ = os.Remove(path)
			continue
		}

		// Skip expired entries
		if !diskEntry.ExpiresAt.IsZero() && now.After(diskEntry.ExpiresAt) {
			_ = os.Remove(path)
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		size := info.Size()
		dc.index[diskEntry.Key] = &diskEntryMeta{
			Key:        diskEntry.Key,
			FilePath:   path,
			Size:       size,
			CreatedAt:  diskEntry.CreatedAt,
			ExpiresAt:  diskEntry.ExpiresAt,
			AccessedAt: now,
		}

		totalSize += size
		count++
	}

	dc.currentSize.Store(totalSize)
	dc.entryCount.Store(count)

	return nil
}

// evictIfNeeded removes entries if cache exceeds size limit.
func (dc *DiskCache) evictIfNeeded() {
	if dc.maxSize <= 0 {
		return
	}

	for dc.currentSize.Load() > dc.maxSize {
		// Find and remove oldest entry
		dc.indexMu.Lock()
		var oldest *diskEntryMeta
		for _, meta := range dc.index {
			if oldest == nil || meta.AccessedAt.Before(oldest.AccessedAt) {
				oldest = meta
			}
		}

		if oldest == nil {
			dc.indexMu.Unlock()
			break
		}

		delete(dc.index, oldest.Key)
		dc.currentSize.Add(-oldest.Size)
		dc.entryCount.Add(-1)
		filePath := oldest.FilePath
		dc.indexMu.Unlock()

		_ = os.Remove(filePath)
		dc.evictions.Add(1)
	}
}

// cleanupLoop runs periodic cleanup of expired entries.
func (dc *DiskCache) cleanupLoop() {
	defer close(dc.cleanupDone)

	ticker := time.NewTicker(dc.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-dc.stopCleanup:
			return
		case <-ticker.C:
			dc.cleanupExpired()
		}
	}
}

// cleanupExpired removes all expired entries.
func (dc *DiskCache) cleanupExpired() {
	now := time.Now()

	dc.indexMu.Lock()
	var expired []*diskEntryMeta
	for key, meta := range dc.index {
		if !meta.ExpiresAt.IsZero() && now.After(meta.ExpiresAt) {
			expired = append(expired, meta)
			delete(dc.index, key)
			dc.currentSize.Add(-meta.Size)
			dc.entryCount.Add(-1)
		}
	}
	dc.indexMu.Unlock()

	// Remove files
	for _, meta := range expired {
		_ = os.Remove(meta.FilePath)
		dc.evictions.Add(1)
	}
}

// GetEntry retrieves the full entry with metadata.
func (dc *DiskCache) GetEntry(key string) (*Entry, bool) {
	dc.indexMu.RLock()
	meta, exists := dc.index[key]
	dc.indexMu.RUnlock()

	if !exists {
		return nil, false
	}

	// Check expiration
	if !meta.ExpiresAt.IsZero() && time.Now().After(meta.ExpiresAt) {
		return nil, false
	}

	entry, err := dc.readEntry(meta.FilePath)
	if err != nil {
		return nil, false
	}

	return &Entry{
		Key:        entry.Key,
		Value:      entry.Value,
		CreatedAt:  entry.CreatedAt,
		AccessedAt: meta.AccessedAt,
		ExpiresAt:  entry.ExpiresAt,
		Size:       int(meta.Size),
	}, true
}

// Import loads cache entries from a reader (for bulk loading).
func (dc *DiskCache) Import(r io.Reader) error {
	decoder := json.NewDecoder(r)

	for {
		var entry diskEntry
		if err := decoder.Decode(&entry); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("failed to decode entry: %w", err)
		}

		// Calculate remaining TTL
		var ttl time.Duration
		if !entry.ExpiresAt.IsZero() {
			remaining := time.Until(entry.ExpiresAt)
			if remaining <= 0 {
				continue // Skip expired
			}
			ttl = remaining
		}

		dc.SetWithTTL(entry.Key, entry.Value, ttl)
	}

	return nil
}

// Export writes all cache entries to a writer (for backup/transfer).
func (dc *DiskCache) Export(w io.Writer) error {
	dc.indexMu.RLock()
	metas := make([]*diskEntryMeta, 0, len(dc.index))
	for _, meta := range dc.index {
		metas = append(metas, meta)
	}
	dc.indexMu.RUnlock()

	encoder := json.NewEncoder(w)
	now := time.Now()

	for _, meta := range metas {
		// Skip expired
		if !meta.ExpiresAt.IsZero() && now.After(meta.ExpiresAt) {
			continue
		}

		entry, err := dc.readEntry(meta.FilePath)
		if err != nil {
			continue
		}

		if err := encoder.Encode(entry); err != nil {
			return fmt.Errorf("failed to encode entry: %w", err)
		}
	}

	return nil
}

// Compact removes all expired entries and defragments storage.
func (dc *DiskCache) Compact() error {
	dc.cleanupExpired()
	return nil
}

//nolint:gochecknoinits // gob type registration must happen before any encoding/decoding operations
func init() {
	// register common types for gob encoding
	gob.Register(map[string]interface{}{})
	gob.Register([]interface{}{})
	gob.Register(map[string]interface{}{})
}

// SerializeValue serializes a value for disk storage.
func SerializeValue(value interface{}) ([]byte, error) {
	var buf bytes.Buffer
	encoder := gob.NewEncoder(&buf)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DeserializeValue deserializes a value from disk storage.
func DeserializeValue(data []byte) (interface{}, error) {
	var value interface{}
	decoder := gob.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}
