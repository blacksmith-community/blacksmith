package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"blacksmith/bosh"
	"blacksmith/shield"
	"code.cloudfoundry.org/lager"
	"github.com/pivotal-cf/brokerapi/v8"
)

// Version gets edited during a release build
var (
	Version   = "(development version)"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

func main() {
	showVersion := flag.Bool("v", false, "Display the version of Blacksmith")
	configPath := flag.String("c", "", "path to config")
	flag.Parse()

	if *showVersion {
		fmt.Printf("blacksmith %s\n", Version)
		fmt.Printf("  Build Time: %s\n", BuildTime)
		fmt.Printf("  Git Commit: %s\n", GitCommit)
		fmt.Printf("  Go Version: %s\n", runtime.Version())
		os.Exit(0)
	}

	config, err := ReadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	if config.Debug {
		Debugging = true
	}

	l := Logger.Wrap("*")

	// Log build version information at startup
	l.Info("blacksmith starting - version: %s, build: %s, commit: %s, go: %s",
		Version, BuildTime, GitCommit, runtime.Version())

	bind := fmt.Sprintf("%s:%s", config.Broker.BindIP, config.Broker.Port)
	l.Info("broker will listen on %s", bind)

	vault := &Vault{
		URL:      config.Vault.Address,
		Token:    "", // will be supplied soon.
		Insecure: config.Vault.Insecure,
	}
	// TLS configuration is now handled by VaultClient internally
	if err = vault.Init(config.Vault.CredPath); err != nil {
		log.Fatal(err)
	}
	if err = vault.VerifyMount("secret", true); err != nil {
		log.Fatal(err)
	}
	// Set BOSH environment variables for CLI compatibility
	if err := os.Setenv("BOSH_CLIENT", config.BOSH.Username); err != nil {
		l.Error("Failed to set BOSH_CLIENT env var: %s", err)
	}
	if err := os.Setenv("BOSH_CLIENT_SECRET", config.BOSH.Password); err != nil {
		l.Error("Failed to set BOSH_CLIENT_SECRET env var: %s", err)
	}
	l.Debug("Set BOSH_CLIENT to: %s", config.BOSH.Username)

	// Create a logger adapter for BOSH operations
	boshLogger := bosh.NewLoggerAdapter(l)
	boshDirector, err := bosh.CreateDirectorWithLogger(
		config.BOSH.Address,
		config.BOSH.Username,
		config.BOSH.Password,
		config.BOSH.CACert,
		config.BOSH.SkipSslValidation,
		boshLogger,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to authenticate to BOSH: %s\n", err)
		os.Exit(2)
	}

	if config.BOSH.CloudConfig != "" {
		// Check if cloud config is effectively empty (just {} or whitespace)
		trimmed := strings.TrimSpace(config.BOSH.CloudConfig)
		if trimmed != "{}" && trimmed != "---" && trimmed != "--- {}" && trimmed != "" {
			l.Info("updating cloud-config...")
			l.Debug("updating cloud-config with:\n%s", config.BOSH.CloudConfig)
			err = boshDirector.UpdateCloudConfig(config.BOSH.CloudConfig)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to update CLOUD-CONFIG: %s\ncloud-config:\n%s\n", err, config.BOSH.CloudConfig)
				os.Exit(2)
			}
		}
	}

	if config.BOSH.Releases != nil {
		rr, err := boshDirector.GetReleases()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to retrieve RELEASES list: %s\n", err)
			os.Exit(2)
		}
		have := make(map[string]bool)
		for _, r := range rr {
			for _, v := range r.ReleaseVersions {
				have[r.Name+"/"+v.Version] = true
			}
		}

		l.Info("uploading releases...")
		for _, r := range config.BOSH.Releases {
			if have[r.Name+"/"+r.Version] {
				l.Info("skipping %s/%s (already uploaded)", r.Name, r.Version)
				continue
			}
			l.Debug("uploading release %s/%s [sha1 %s] from %s", r.Name, r.Version, r.SHA1, r.URL)
			task, err := boshDirector.UploadRelease(r.URL, r.SHA1)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nFailed to upload RELEASE (%s) sha1 [%s]: %s\n", r.URL, r.SHA1, err)
				os.Exit(2)
			}
			l.Info("uploading release %s/%s [sha1 %s] in BOSH task %d, from %s", r.Name, r.Version, r.SHA1, task.ID, r.URL)
		}
	}

	if config.BOSH.Stemcells != nil {
		ss, err := boshDirector.GetStemcells()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to retrieve STEMCELLS list: %s\n", err)
			os.Exit(2)
		}
		have := make(map[string]bool)
		for _, sc := range ss {
			have[sc.Name+"/"+sc.Version] = true
		}

		l.Info("uploading stemcells...")
		for _, sc := range config.BOSH.Stemcells {
			if have[sc.Name+"/"+sc.Version] {
				l.Info("skipping %s/%s (already uploaded)", sc.Name, sc.Version)
				continue
			}
			l.Debug("uploading stemcell %s/%s [sha1 %s] from %s", sc.Name, sc.Version, sc.SHA1, sc.URL)
			task, err := boshDirector.UploadStemcell(sc.URL, sc.SHA1)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nFailed to upload STEMCELL (%s) sha1 [%s]: %s\n", err, sc.URL, sc.SHA1)
				os.Exit(2)
			}
			l.Info("uploading stemcell %s/%s [sha1 %s] in BOSH task %d, from %s", sc.Name, sc.Version, sc.SHA1, task.ID, sc.URL)
		}
	}

	var shieldClient shield.Client = &shield.NoopClient{}
	if config.Shield.Enabled {
		cfg := shield.Config{
			Address:  config.Shield.Address,
			Insecure: config.Shield.Insecure,

			Agent: config.Shield.Agent,

			Tenant: config.Shield.Tenant,
			Store:  config.Shield.Store,

			Schedule: config.Shield.Schedule,
			Retain:   config.Shield.Retain,

			EnabledOnTargets: config.Shield.EnabledOnTargets,
		}

		if cfg.Schedule == "" {
			cfg.Schedule = "daily 6am"
		}
		if cfg.Retain == "" {
			cfg.Retain = "7d"
		}

		switch config.Shield.AuthMethod {
		case "local":
			cfg.Authentication = &shield.LocalAuth{Username: config.Shield.Username, Password: config.Shield.Password}
		case "token":
			cfg.Authentication = &shield.TokenAuth{Token: config.Shield.Token}
		default:
			fmt.Fprintf(os.Stderr, "Invalid S.H.I.E.L.D. authentication method (must be one of 'local' or 'token'): %s\n", config.Shield.AuthMethod)
			os.Exit(2)
		}

		l.Debug("creating S.H.I.E.L.D. client with config: %+v", cfg)
		shieldClient, err = shield.NewClient(cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create S.H.I.E.L.D. client: %s\n", err)
			os.Exit(2)
		}
	}

	broker := &Broker{
		Vault:  vault,
		BOSH:   boshDirector,
		Shield: shieldClient,
	}

	l.Info("reading services from %s", strings.Join(os.Args[3:], ", "))
	err = broker.ReadServices(os.Args[3:]...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read SERVICE directories: %s\n", err)
		os.Exit(2)
	}

	var ui http.Handler = NullHandler{}
	if config.WebRoot != "" {
		ui = http.FileServer(http.Dir(config.WebRoot))
	}
	http.Handle("/", &API{
		Username: config.Broker.Username,
		Password: config.Broker.Password,
		WebRoot:  ui,
		Internal: &InternalApi{
			Env:    config.Env,
			Vault:  vault,
			Broker: broker,
		},
		Primary: brokerapi.New(
			broker,
			lager.NewLogger("blacksmith-broker"),
			brokerapi.BrokerCredentials{
				Username: config.Broker.Username,
				Password: config.Broker.Password,
			},
		),
	})

	l.Info("blacksmith service broker v%s starting up...", Version)

	// Create HTTP server with proper timeouts for service broker operations
	// Service provisioning can take longer due to BOSH operations (manifest generation, release uploads, etc.)
	// Default timeouts if not specified in config
	readTimeout := 120
	if config.Broker.ReadTimeout > 0 {
		readTimeout = config.Broker.ReadTimeout
	}
	writeTimeout := 120
	if config.Broker.WriteTimeout > 0 {
		writeTimeout = config.Broker.WriteTimeout
	}
	idleTimeout := 300
	if config.Broker.IdleTimeout > 0 {
		idleTimeout = config.Broker.IdleTimeout
	}

	l.Info("HTTP server timeouts - Read: %ds, Write: %ds, Idle: %ds", readTimeout, writeTimeout, idleTimeout)

	server := &http.Server{
		Addr:         bind,
		Handler:      nil, // Uses http.DefaultServeMux
		ReadTimeout:  time.Duration(readTimeout) * time.Second,
		WriteTimeout: time.Duration(writeTimeout) * time.Second,
		IdleTimeout:  time.Duration(idleTimeout) * time.Second, // Increased from 60s for long-running connections
	}

	go func() {
		err := server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			l.Error("blacksmith service broker failed to start up: %s", err)
			os.Exit(2)
		}
		l.Info("shutting down blacksmith service broker")
	}()

	BoshMaintenanceLoop := time.NewTicker(1 * time.Hour)
	//TODO set task to -1 or something out here and check to make sure the cleanup is finished before you run another one
	for {
		select {
		case <-BoshMaintenanceLoop.C:
			vaultDB, err := vault.getVaultDB()
			if err != nil {
				l.Error("error grabbing vaultdb for debugging: %s", err)
			}
			l.Info("current vault db looks like: %v", vaultDB.Data)
			broker.serviceWithNoDeploymentCheck()
			//			Disable cleanup process, using external bosh director
			//			task, err := bosh.Cleanup(false)
			//			l.Info("taskid for the bosh cleanup is %v", task.ID)
			//			if err != nil {
			//				l.Error("bosh cleanup failed to run properly: %s", err)
			//			}
		}
	}
}
