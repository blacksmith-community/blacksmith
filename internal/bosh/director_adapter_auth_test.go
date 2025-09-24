package bosh

import (
	"errors"
	"testing"

	boshdirector "github.com/cloudfoundry/bosh-cli/v7/director"
)

type fakeDirectorInfoProvider struct {
	info boshdirector.Info
	err  error
}

func (f *fakeDirectorInfoProvider) Info() (boshdirector.Info, error) {
	return f.info, f.err
}

func TestSetupUAAAuthWithoutExplicitUAAConfig(t *testing.T) {
	t.Parallel()

	directorInfo := boshdirector.Info{
		Auth: boshdirector.UserAuthentication{
			Type: authTypeUAA,
			Options: map[string]interface{}{
				"url": "https://uaa.example.com:8443",
			},
		},
	}

	fakeDirector := &fakeDirectorInfoProvider{info: directorInfo}

	factoryConfig := &boshdirector.FactoryConfig{Host: "director.example.com", Port: 25555}

	config := Config{
		Address:  "https://director.example.com:25555",
		Username: "client-id",
		Password: "client-secret",
	}

	updatedConfig, err := setupUAAAuth(config, fakeDirector, factoryConfig)
	if err != nil {
		t.Fatalf("setupUAAAuth returned error: %v", err)
	}

	if updatedConfig.TokenFunc == nil {
		t.Fatalf("expected TokenFunc to be configured")
	}
}

func TestSetupUAAAuthMissingCredentials(t *testing.T) {
	t.Parallel()

	directorInfo := boshdirector.Info{
		Auth: boshdirector.UserAuthentication{
			Type: authTypeUAA,
			Options: map[string]interface{}{
				"url": "https://uaa.example.com",
			},
		},
	}

	fakeDirector := &fakeDirectorInfoProvider{info: directorInfo}

	factoryConfig := &boshdirector.FactoryConfig{Host: "director.example.com", Port: 25555}

	config := Config{
		Address: "https://director.example.com:25555",
	}

	_, err := setupUAAAuth(config, fakeDirector, factoryConfig)
	if err == nil {
		t.Fatalf("expected error when no UAA client credentials are provided")
	}

	if !errors.Is(err, ErrUAAClientCredentialsMissing) {
		t.Fatalf("expected ErrUAAClientCredentialsMissing but got %v", err)
	}
}
