package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// FileStore is a minimal persistent key-value store for short-lived CLI
// processes: one file per entry, named by the SHA-256 of the key, written
// atomically via temp-file-and-rename, expired by file mtime on lookup.
//
// It deliberately keeps no in-memory index: DiskCache's loadIndex reads
// and decodes every entry in the directory at construction time, which is
// the right trade for a long-lived process but makes every invocation of
// a short-lived CLI pay for the whole cache. A FileStore lookup touches
// exactly one file whether the store holds one entry or ten thousand.
type FileStore struct {
	dir string
	ttl time.Duration
}

// OpenFileStore opens (creating if needed) a FileStore rooted at dir.
// A ttl of zero means entries never expire.
func OpenFileStore(dir string, ttl time.Duration) (*FileStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}
	return &FileStore{dir: dir, ttl: ttl}, nil
}

// Dir returns the directory the store keeps its entries in.
func (s *FileStore) Dir() string {
	return s.dir
}

// keyPath maps an arbitrary key string to its entry's file path. Hashing
// makes any key filename-safe and confines every entry to the store dir.
func (s *FileStore) keyPath(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.dir, hex.EncodeToString(sum[:])+".v1")
}

// Get returns the bytes stored under key, or ok=false on a miss. An entry
// older than the store TTL is removed and reported as a miss.
func (s *FileStore) Get(key string) ([]byte, bool) {
	path := s.keyPath(key)

	if s.ttl > 0 {
		info, err := os.Stat(path)
		if err != nil {
			return nil, false
		}
		if time.Since(info.ModTime()) > s.ttl {
			_ = os.Remove(path)
			return nil, false
		}
	}

	// #nosec G304 - path is internally generated from a SHA-256 hash
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return data, true
}

// Put stores data under key, atomically: a concurrent reader sees either
// the previous entry or the new one, never a partial write.
func (s *FileStore) Put(key string, data []byte) error {
	tmp, err := os.CreateTemp(s.dir, ".put-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.keyPath(key)); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}
	return nil
}

// Clear removes every entry in the store.
func (s *FileStore) Clear() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Stats returns the number of entries in the store and their total size
// in bytes. In-flight temp files (dot-prefixed) are not counted.
func (s *FileStore) Stats() (count int, size int64, err error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return 0, 0, err
	}
	for _, entry := range entries {
		if entry.IsDir() || (entry.Name() != "" && entry.Name()[0] == '.') {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		count++
		size += info.Size()
	}
	return count, size, nil
}
