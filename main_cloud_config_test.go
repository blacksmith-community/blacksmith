package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"blacksmith/internal/config"
)

const (
	bootBrokerContent  = "azs:\n- name: z1\nvm_types:\n- name: small\ncompilation:\n  workers: 2\n"
	bootWriteTypeCloud = "cloud"
	bootCredential     = "admin"
)

// bootDirector is a test director that holds the given cloud configs and
// records the config writes it receives.
type bootDirector struct {
	*boshtest.Director

	mu     sync.Mutex
	writes []map[string]string
}

func (b *bootDirector) posted() []map[string]string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]map[string]string(nil), b.writes...)
}

// cloudEntry is one cloud config as the director lists it.
func cloudEntry(name, content string) map[string]interface{} {
	return map[string]interface{}{"id": "1", "type": bootWriteTypeCloud, "name": name, "content": content}
}

// newBootDirector starts a director that answers GET /configs with the given
// entries, or with a 500 when listFails is set, and accepts config writes.
func newBootDirector(t *testing.T, listFails bool, entries ...map[string]interface{}) (*bootDirector, bosh.Director, *config.Config) {
	t.Helper()

	boot := &bootDirector{}

	boot.Director = boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/configs" {
			return false
		}

		switch request.Method {
		case http.MethodGet:
			if listFails {
				boshtest.WriteError(writer, http.StatusInternalServerError, 100, "the database is down")

				return true
			}

			list := make([]interface{}, 0, len(entries))
			for _, entry := range entries {
				list = append(list, entry)
			}

			boshtest.WriteJSON(writer, http.StatusOK, list)
		case http.MethodPost:
			raw, _ := io.ReadAll(request.Body)

			var body map[string]string

			_ = json.Unmarshal(raw, &body)

			boot.mu.Lock()
			boot.writes = append(boot.writes, body)
			boot.mu.Unlock()

			boshtest.WriteJSON(writer, http.StatusCreated, cloudEntry(body["name"], body["content"]))
		default:
			return false
		}

		return true
	})

	director, err := bosh.NewDirectorAdapter(bosh.Config{
		Address:  boot.URL,
		Username: bootCredential,
		Password: bootCredential,
		CACert:   boot.CACert,
	})
	if err != nil {
		t.Fatalf("failed to create director adapter: %v", err)
	}

	cfg := &config.Config{}
	cfg.BOSH.Address = boot.URL
	cfg.BOSH.CCPath = "/etc/blacksmith/cloud-config.yml"
	cfg.BOSH.CloudConfig = bootBrokerContent

	return boot, director, cfg
}

func requireNoCloudConfigRoute(t *testing.T, boot *bootDirector) {
	t.Helper()

	for _, request := range boot.Requests() {
		if strings.HasPrefix(request.Path, "/cloud_configs") {
			t.Errorf("expected nothing sent to /cloud_configs, got %s %s", request.Method, request.Path)
		}
	}
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") }

func requireContains(t *testing.T, text string, wants ...string) {
	t.Helper()

	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("expected the log to mention %q, got:\n%s", want, text)
		}
	}
}

func TestBootCloudConfigWritesWhenNothingOverlaps(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	boot, director, cfg := newBootDirector(t, false,
		cloudEntry("", "azs:\n- name: z9\nvm_types:\n- name: big\nnetworks:\n- name: n\n"))
	logger := newRecordingLogger()

	updateBOSHCloudConfig(t.Context(), cfg, director, logger)

	writes := boot.posted()
	if len(writes) != 1 {
		t.Fatalf("expected one write, got %d", len(writes))
	}

	if writes[0]["type"] != bootWriteTypeCloud || writes[0]["name"] != "blacksmith" || writes[0]["content"] != bootBrokerContent {
		t.Errorf("unexpected write: %v", writes[0])
	}

	requireNoCloudConfigRoute(t, boot)
	requireContains(t, joinLines(logger.lines("info")), "blacksmith", "cloud", boot.URL)

	if errs := logger.lines("error"); len(errs) != 0 {
		t.Errorf("expected no errors, got %q", errs)
	}
}

func TestBootCloudConfigSkipsEmptyContent(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	for _, content := range []string{"", "---", "{}", "--- {}", "[]", "--- []", "~"} {
		t.Run("content "+content, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			boot, director, cfg := newBootDirector(t, false)
			cfg.BOSH.CloudConfig = content
			logger := newRecordingLogger()

			updateBOSHCloudConfig(t.Context(), cfg, director, logger)

			if len(boot.posted()) != 0 {
				t.Errorf("expected no write for %q", content)
			}

			if len(boot.Requests()) != 0 {
				t.Errorf("expected the director to hear nothing for %q, got %v", content, boot.Requests())
			}

			if errs := logger.lines("error"); len(errs) != 0 {
				t.Errorf("expected no errors, got %q", errs)
			}
		})
	}
}

func TestBootCloudConfigRejectsUnparseableContent(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	boot, director, cfg := newBootDirector(t, false)
	cfg.BOSH.CloudConfig = "azs: [unclosed"
	logger := newRecordingLogger()

	updateBOSHCloudConfig(t.Context(), cfg, director, logger)

	if len(boot.Requests()) != 0 {
		t.Errorf("expected nothing sent to the director, got %v", boot.Requests())
	}

	errs := joinLines(logger.lines("error"))
	requireContains(t, errs, "Configuration error", cfg.BOSH.CCPath, "not valid YAML", "yaml:")
}

func TestBootCloudConfigSkipsWhenTheDefaultHoldsTheSameContent(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	same := "compilation: {workers: 2}\nvm_types: [ {name: small} ]\nazs:\n    - {name: z1}\n"
	boot, director, cfg := newBootDirector(t, false, cloudEntry("", same))
	logger := newRecordingLogger()

	updateBOSHCloudConfig(t.Context(), cfg, director, logger)

	if len(boot.posted()) != 0 {
		t.Errorf("expected no write, got %v", boot.posted())
	}

	requireContains(t, joinLines(logger.lines("info")), "already lives in", "default cloud config", "no write is needed", boot.URL)
}

func TestBootCloudConfigRefusesOverlappingNames(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	for _, key := range []string{"azs", "vm_types", "disk_types", "networks", "vm_extensions"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("BLACKSMITH_TEST_MODE", "true")

			other := key + ":\n- name: shared\n- name: theirs\n"
			boot, director, cfg := newBootDirector(t, false, cloudEntry("kit", other))
			cfg.BOSH.CloudConfig = key + ":\n- name: shared\n- name: mine\n"
			logger := newRecordingLogger()

			updateBOSHCloudConfig(t.Context(), cfg, director, logger)

			if len(boot.posted()) != 0 {
				t.Errorf("expected no write, got %v", boot.posted())
			}

			requireNoCloudConfigRoute(t, boot)
			requireContains(t, joinLines(logger.lines("error")),
				boot.URL, `"kit"`, key, `"shared"`, "compilation", "twice", "Remove those entries")
		})
	}
}

func TestBootCloudConfigRefusesASecondCompilationBlock(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	boot, director, cfg := newBootDirector(t, false,
		cloudEntry("", "compilation: {workers: 5}\nazs: [{name: z9}]\n"))
	logger := newRecordingLogger()

	updateBOSHCloudConfig(t.Context(), cfg, director, logger)

	if len(boot.posted()) != 0 {
		t.Errorf("expected no write, got %v", boot.posted())
	}

	requireContains(t, joinLines(logger.lines("error")), boot.URL, "default cloud config", "compilation", "Remove those entries")
}

func TestBootCloudConfigSkipsWhenTheConfigsCannotBeRead(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	boot, director, cfg := newBootDirector(t, true)
	logger := newRecordingLogger()

	updateBOSHCloudConfig(t.Context(), cfg, director, logger)

	if len(boot.posted()) != 0 {
		t.Errorf("expected no write, got %v", boot.posted())
	}

	requireContains(t, joinLines(logger.lines("error")),
		boot.URL, "500", "the database is down", "blind write", "could break the director's deploys")
}

func TestBootCloudConfigFailedWriteIsNotFatalAndNamesTheDirector(t *testing.T) {
	t.Setenv("BLACKSMITH_TEST_MODE", "true")

	server := boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/configs" {
			return false
		}

		if request.Method == http.MethodGet {
			boshtest.WriteJSON(writer, http.StatusOK, []interface{}{})
		} else {
			boshtest.WriteError(writer, http.StatusBadRequest, 440001, "bad cloud config")
		}

		return true
	})

	director, err := bosh.NewDirectorAdapter(bosh.Config{Address: server.URL, Username: bootCredential, Password: bootCredential, CACert: server.CACert})
	if err != nil {
		t.Fatalf("failed to create director adapter: %v", err)
	}

	cfg := &config.Config{}
	cfg.BOSH.Address = server.URL
	cfg.BOSH.CloudConfig = bootBrokerContent
	logger := newRecordingLogger()

	updateBOSHCloudConfig(t.Context(), cfg, director, logger)

	requireContains(t, joinLines(logger.lines("error")), server.URL, "400", "bad cloud config")
}
