package main

import (
	"errors"
	"testing"

	boshhandler "blacksmith/internal/handlers/bosh"
	vaultapi "github.com/hashicorp/vault/api"
)

var errVaultDown = errors.New("connection refused")

func TestClassifyVaultHealth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		health *vaultapi.HealthResponse
		err    error
		want   error
	}{
		{"an unsealed vault", &vaultapi.HealthResponse{Initialized: true}, nil, nil},
		{"a sealed vault", &vaultapi.HealthResponse{Initialized: true, Sealed: true}, nil, boshhandler.ErrVaultSealed},
		{"a vault that does not answer", nil, errVaultDown, errVaultDown},
		{"an uninitialized vault", &vaultapi.HealthResponse{}, nil, errVaultNotInitialized},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got := classifyVaultHealth(test.health, test.err)
			if !errors.Is(got, test.want) || (test.want == nil && got != nil) {
				t.Fatalf("expected %v, got %v", test.want, got)
			}

			if errors.Is(got, boshhandler.ErrVaultSealed) != errors.Is(test.want, boshhandler.ErrVaultSealed) {
				t.Fatalf("only a sealed vault may report sealed, got %v", got)
			}
		})
	}
}
