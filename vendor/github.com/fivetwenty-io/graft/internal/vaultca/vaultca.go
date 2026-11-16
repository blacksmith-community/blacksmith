// Package vaultca loads the certificate authorities a Vault client trusts
// from a PEM file, PEM bytes, or a directory of PEM files. It follows the
// semantics of Vault's own api.TLSConfig (CACert, CACertBytes, CAPath):
// the first configured source wins, and its certificates replace the
// system roots rather than adding to them.
//
// Both vault client builders use it: pkg/graft's WithVault backend and
// internal/backends/vault's environment-configured reader. It imports
// neither, so neither import direction creates a cycle.
package vaultca

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// ErrCA is the sentinel every CA loading failure matches with errors.Is.
var ErrCA = errors.New("vault: CA certificate configuration")

// CAError reports why a configured CA source could not be loaded. Its
// message names the field or environment variable and the path, and
// never includes anything read from the file or the PEM bytes.
type CAError struct {
	// Field names the setting that supplied the source, such as
	// "VaultConfig.CACert" or "VAULT_CACERT".
	Field string
	// Path is the file or directory that failed, or "" for PEM bytes.
	Path string
	// Reason describes the failure in a few words.
	Reason string
	// Err is the underlying error, such as an *fs.PathError, or nil.
	Err error
}

// Error renders `vault: <Field> "<Path>": <Reason>[: <cause>]`. A
// *fs.PathError cause is shortened to its operation and errno, since the
// path already appears once.
func (e *CAError) Error() string {
	s := "vault: " + e.Field
	if e.Path != "" {
		s += fmt.Sprintf(" %q", e.Path)
	}

	s += ": " + e.Reason

	var pathErr *fs.PathError

	switch {
	case e.Err == nil:
	case errors.As(e.Err, &pathErr):
		s += ": " + pathErr.Op + ": " + pathErr.Err.Error()
	default:
		s += ": " + e.Err.Error()
	}

	return s
}

// Unwrap returns the underlying error.
func (e *CAError) Unwrap() error { return e.Err }

// Is reports whether target is ErrCA.
func (e *CAError) Is(target error) bool { return target == ErrCA }

// Source holds the three CA settings and the names that error messages
// use for them. An empty string or a zero-length slice counts as unset.
type Source struct {
	// CACert is the path of a PEM file of CA certificates.
	CACert string
	// CACertBytes holds PEM-encoded CA certificates.
	CACertBytes []byte
	// CAPath is a directory walked recursively for PEM files.
	CAPath string

	// CACertField, CACertBytesField, and CAPathField name each setting
	// in error messages.
	CACertField      string
	CACertBytesField string
	CAPathField      string
}

// Load returns a pool holding only the certificates from the first set
// source, checked in the order CACert, CACertBytes, CAPath; the other
// sources are ignored and never read. It returns a nil pool and a nil
// error when no source is set, so the caller keeps the system roots.
// Every failure is a *CAError matching ErrCA.
func Load(src Source) (*x509.CertPool, error) {
	pool := x509.NewCertPool()

	var err error

	switch {
	case src.CACert != "":
		err = loadFile(pool, src.CACert, src.CACertField)
	case len(src.CACertBytes) > 0:
		if !pool.AppendCertsFromPEM(src.CACertBytes) {
			err = &CAError{Field: src.CACertBytesField, Reason: noCertReason}
		}
	case src.CAPath != "":
		err = loadDir(pool, src.CAPath, src.CAPathField)
	default:
		return nil, nil
	}

	if err != nil {
		return nil, err
	}

	return pool, nil
}

const noCertReason = "holds no parseable PEM certificate"

// loadFile appends the certificates in the PEM file at path to pool.
func loadFile(pool *x509.CertPool, path, field string) error {
	// The path is operator-supplied CA configuration, read on purpose.
	data, err := os.ReadFile(path) // #nosec G304 -- configured CA file
	if err != nil {
		return &CAError{Field: field, Path: path, Reason: "cannot read CA certificate", Err: err}
	}

	if !pool.AppendCertsFromPEM(data) {
		return &CAError{Field: field, Path: path, Reason: noCertReason}
	}

	return nil
}

// loadDir walks dir recursively, as Vault's go-rootcerts does, and
// appends every regular file's certificates to pool. A symlink to a
// regular file is loaded; a symlink to a directory is not followed, so a
// link cycle cannot loop. dir itself may be a symlink, and a CAPath that
// names a regular file loads that file, which Vault also accepts.
func loadDir(pool *x509.CertPool, dir, field string) error {
	info, err := os.Stat(dir)
	if err != nil {
		return &CAError{Field: field, Path: dir, Reason: "cannot read CA directory", Err: err}
	}

	if !info.IsDir() {
		return loadFile(pool, dir, field)
	}

	loaded := 0

	err = fs.WalkDir(os.DirFS(dir), ".", func(rel string, d fs.DirEntry, err error) error {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err != nil {
			return &CAError{Field: field, Path: path, Reason: "cannot read CA directory", Err: err}
		}

		if !isRegular(path, d) {
			return nil
		}

		if err := loadFile(pool, path, field); err != nil {
			return err
		}

		loaded++

		return nil
	})
	if err != nil {
		return err
	}

	if loaded == 0 {
		return &CAError{Field: field, Path: dir, Reason: "holds no CA certificate files"}
	}

	return nil
}

// isRegular reports whether the walked entry is a regular file or a
// symlink that resolves to one. A dangling symlink counts as a file, so
// loadFile reports it rather than letting it vanish silently.
func isRegular(path string, d fs.DirEntry) bool {
	if d.Type().IsRegular() {
		return true
	}

	if d.Type()&fs.ModeSymlink == 0 {
		return false
	}

	info, err := os.Stat(path)
	if err != nil {
		return true
	}

	return info.Mode().IsRegular()
}
