package credhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"
)

const (
	opCertificates     = "certificate metadata"
	opCredentialList   = "credential versions"
	opCertificateRead  = "certificate version"
	certificatesPath   = "/api/v1/certificates"
	maxMetadataBody    = 1 << 20
	maxVersionsBody    = 512 << 10
	maxCertificateBody = 64 << 10
	minVersionsLimit   = 1
	maxVersionsLimit   = 20
)

// CertificateVersionMeta is what CredHub says about one version of a
// certificate, without any of its content.
type CertificateVersionMeta struct {
	ID                   string
	ExpiryDate           time.Time
	Transitional         bool
	CertificateAuthority bool
	SelfSigned           bool
	Generated            bool
}

// CertificateMeta is what CredHub says about a certificate credential, with
// its versions as CredHub lists them.
type CertificateMeta struct {
	ID       string
	Name     string
	SignedBy string
	Versions []CertificateVersionMeta
}

// CredentialVersion is one version of any credential. It holds no value.
type CredentialVersion struct {
	ID        string
	Type      string
	CreatedAt time.Time
}

// CertificatePublic is the public half of one certificate version. It never
// holds the private key.
type CertificatePublic struct {
	VersionID   string
	Name        string
	Certificate string
	CA          string
	CreatedAt   time.Time
}

type certificatesAnswer struct {
	Certificates []struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		SignedBy string `json:"signed_by"`
		Versions []struct {
			ID                   string    `json:"id"`
			ExpiryDate           time.Time `json:"expiry_date"`
			Transitional         bool      `json:"transitional"`
			CertificateAuthority bool      `json:"certificate_authority"`
			SelfSigned           bool      `json:"self_signed"`
			Generated            bool      `json:"generated"`
		} `json:"versions"`
	} `json:"certificates"`
}

// versionsAnswer names no value field, so encoding/json skips every value
// without keeping it.
type versionsAnswer struct {
	Data []struct {
		ID               string    `json:"id"`
		Type             string    `json:"type"`
		VersionCreatedAt time.Time `json:"version_created_at"`
	} `json:"data"`
}

// certificateAnswer names the certificate and the CA only, so the private
// key in the same value is skipped without being kept.
type certificateAnswer struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	VersionCreatedAt time.Time `json:"version_created_at"`
	Value            struct {
		Certificate string `json:"certificate"`
		CA          string `json:"ca"`
	} `json:"value"`
}

// CertificateMetadata returns what CredHub knows about the certificate with
// exactly this name, or ErrNotFound when it has none.
func (c *Client) CertificateMetadata(ctx context.Context, name string) (*CertificateMeta, error) {
	body, err := c.send(ctx, request{
		operation:    opCertificates,
		method:       http.MethodGet,
		path:         certificatesPath,
		query:        url.Values{"name": {name}},
		maxBody:      maxMetadataBody,
		opaqueErrors: true,
	})
	if err != nil {
		return nil, notFoundOn404(err, name)
	}

	var parsed certificatesAnswer

	err = decode(opCertificates, body, &parsed)
	if err != nil {
		return nil, err
	}

	if len(parsed.Certificates) == 0 {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}

	found := parsed.Certificates[0]
	meta := &CertificateMeta{ID: found.ID, Name: found.Name, SignedBy: found.SignedBy}

	for _, version := range found.Versions {
		meta.Versions = append(meta.Versions, CertificateVersionMeta{
			ID:                   version.ID,
			ExpiryDate:           version.ExpiryDate,
			Transitional:         version.Transitional,
			CertificateAuthority: version.CertificateAuthority,
			SelfSigned:           version.SelfSigned,
			Generated:            version.Generated,
		})
	}

	return meta, nil
}

// CredentialVersions returns up to limit versions of any credential, newest
// first, with no value. limit is held between 1 and 20. The creation times
// are the authoritative order of versions.
func (c *Client) CredentialVersions(ctx context.Context, name string, limit int) ([]CredentialVersion, error) {
	limit = min(max(limit, minVersionsLimit), maxVersionsLimit)

	body, err := c.send(ctx, request{
		operation:    opCredentialList,
		method:       http.MethodGet,
		path:         dataPath,
		query:        url.Values{"name": {name}, "versions": {strconv.Itoa(limit)}},
		maxBody:      maxVersionsBody,
		opaqueErrors: true,
	})
	if err != nil {
		return nil, notFoundOn404(err, name)
	}

	var parsed versionsAnswer

	err = decode(opCredentialList, body, &parsed)
	if err != nil {
		return nil, err
	}

	versions := make([]CredentialVersion, 0, len(parsed.Data))
	for _, entry := range parsed.Data {
		versions = append(versions, CredentialVersion{ID: entry.ID, Type: entry.Type, CreatedAt: entry.VersionCreatedAt})
	}

	sort.SliceStable(versions, func(i, j int) bool { return versions[i].CreatedAt.After(versions[j].CreatedAt) })

	return versions, nil
}

// CertificateVersion returns the certificate and the CA of one version, and
// never its private key.
func (c *Client) CertificateVersion(ctx context.Context, versionID string) (*CertificatePublic, error) {
	if versionID == "" {
		return nil, ErrEmptyVersionID
	}

	body, err := c.send(ctx, request{
		operation:    opCertificateRead,
		method:       http.MethodGet,
		path:         dataPath + "/" + url.PathEscape(versionID),
		maxBody:      maxCertificateBody,
		opaqueErrors: true,
	})
	if err != nil {
		return nil, notFoundOn404(err, versionID)
	}

	var parsed certificateAnswer

	err = decode(opCertificateRead, body, &parsed)
	if err != nil {
		return nil, err
	}

	id := parsed.ID
	if id == "" {
		id = versionID
	}

	return &CertificatePublic{
		VersionID:   id,
		Name:        parsed.Name,
		Certificate: parsed.Value.Certificate,
		CA:          parsed.Value.CA,
		CreatedAt:   parsed.VersionCreatedAt,
	}, nil
}

// decode reads body into target and then overwrites body with zeros, so a
// key in the buffer does not outlive the call. A failure names the
// operation and the type of the decode error, never the body or the decode
// error's text, which can quote it.
func decode(operation string, body []byte, target any) error {
	err := json.Unmarshal(body, target)

	clear(body)

	if err != nil {
		return fmt.Errorf("CredHub %s: the answer could not be decoded (%T)", operation, err)
	}

	return nil
}

func notFoundOn404(err error, name string) error {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return fmt.Errorf("%w: %q", ErrNotFound, name)
	}

	return err
}
