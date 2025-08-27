package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"blacksmith/bosh"
	"blacksmith/bosh/ssh"
	"blacksmith/pkg/services"
	"blacksmith/services/rabbitmq"
	"blacksmith/shield"
	"blacksmith/websocket"
	"code.cloudfoundry.org/lager"
	"github.com/pivotal-cf/brokerapi/v8"
)

// Version gets edited during a release build
var (
	Version   = "(development version)"
	BuildTime = "unknown"
	GitCommit = "unknown"
)

// createHTTPSRedirectHandler creates an HTTP handler that redirects all requests to HTTPS
func createHTTPSRedirectHandler(httpsPort string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if strings.Contains(host, ":") {
			host = strings.Split(host, ":")[0]
		}

		redirectURL := fmt.Sprintf("https://%s:%s%s", host, httpsPort, r.RequestURI)
		http.Redirect(w, r, redirectURL, http.StatusMovedPermanently)
	}
}

// startHTTPServer starts an HTTP server, either for redirects (when TLS enabled) or normal operation
func startHTTPServer(config *Config, handler http.Handler, l *Log) *http.Server {
	bind := fmt.Sprintf("%s:%s", config.Broker.BindIP, config.Broker.Port)

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

	var httpHandler http.Handler
	if config.Broker.TLS.Enabled {
		// When TLS is enabled, HTTP server only handles redirects
		httpHandler = createHTTPSRedirectHandler(config.Broker.TLS.Port)
		l.Info("HTTP server on %s will redirect to HTTPS port %s", bind, config.Broker.TLS.Port)
	} else {
		// When TLS is disabled, HTTP server handles normal traffic
		// Apply compression middleware if enabled
		compressionConfig := config.Broker.Compression
		if compressionConfig.Enabled {
			httpHandler = NewCompressionMiddleware(handler, compressionConfig)
			l.Info("HTTP server compression enabled with types: %v", compressionConfig.Types)
		} else {
			httpHandler = handler
		}
		l.Info("HTTP server will listen on %s", bind)
	}

	server := &http.Server{
		Addr:         bind,
		Handler:      httpHandler,
		ReadTimeout:  time.Duration(readTimeout) * time.Second,
		WriteTimeout: time.Duration(writeTimeout) * time.Second,
		IdleTimeout:  time.Duration(idleTimeout) * time.Second,
	}

	return server
}

// startHTTPSServer starts an HTTPS server with TLS configuration
func startHTTPSServer(config *Config, handler http.Handler, l *Log) (*http.Server, error) {
	if !config.Broker.TLS.Enabled {
		return nil, nil
	}

	tlsConfig, err := CreateTLSConfig(config.Broker.TLS)
	if err != nil {
		return nil, fmt.Errorf("failed to create TLS configuration: %w", err)
	}

	bind := fmt.Sprintf("%s:%s", config.Broker.BindIP, config.Broker.TLS.Port)
	l.Info("HTTPS server will listen on %s", bind)

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

	// Apply compression middleware if enabled
	var httpsHandler http.Handler
	compressionConfig := config.Broker.Compression
	if compressionConfig.Enabled {
		httpsHandler = NewCompressionMiddleware(handler, compressionConfig)
		l.Info("HTTPS server compression enabled with types: %v", compressionConfig.Types)
	} else {
		httpsHandler = handler
	}

	server := &http.Server{
		Addr:         bind,
		Handler:      httpsHandler,
		TLSConfig:    tlsConfig,
		ReadTimeout:  time.Duration(readTimeout) * time.Second,
		WriteTimeout: time.Duration(writeTimeout) * time.Second,
		IdleTimeout:  time.Duration(idleTimeout) * time.Second,
	}

	return server, nil
}

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

	// TLS configuration validation
	if config.Broker.TLS.Enabled {
		if err := ValidateCertificateFiles(config.Broker.TLS.Certificate, config.Broker.TLS.Key); err != nil {
			l.Error("TLS configuration error: %s", err)
			os.Exit(2)
		}
		l.Info("TLS enabled - certificate: %s, key: %s", config.Broker.TLS.Certificate, config.Broker.TLS.Key)
	}

	vault := &Vault{
		URL:      config.Vault.Address,
		Token:    "", // will be supplied soon.
		Insecure: config.Vault.Insecure,
	}

	// Wait for Vault to be ready before proceeding
	if err = vault.WaitForVaultReady(); err != nil {
		l.Error("Vault readiness check failed: %s", err)
		log.Fatal(err)
	}

	// TLS configuration is now handled by VaultClient internally
	if err = vault.Init(config.Vault.CredPath); err != nil {
		log.Fatal(err)
	}
	if err = vault.VerifyMount("secret", true); err != nil {
		log.Fatal(err)
	}

	// Store blacksmith plans to Vault after ensuring KVv2
	planStorage := NewPlanStorage(vault, &config)
	if err = planStorage.StorePlans(); err != nil {
		l.Error("Failed to store blacksmith plans to Vault: %s", err)
		// Don't fail startup if plan storage fails
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
		Config: &config,
	}

	// Read services from CLI args or auto-scan
	if len(os.Args) > 3 {
		l.Info("reading services from CLI arguments: %s", strings.Join(os.Args[3:], ", "))
		err = broker.ReadServices(os.Args[3:]...)
	} else {
		l.Info("no CLI arguments provided, using configuration-based service discovery")
		err = broker.ReadServices()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read SERVICE directories: %s\n", err)
		os.Exit(2)
	}

	var ui http.Handler = NullHandler{}
	if config.WebRoot != "" {
		ui = http.FileServer(http.Dir(config.WebRoot))
	}

	l.Info("blacksmith service broker v%s starting up...", Version)

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Initialize CF connection manager
	var cfManager *CFConnectionManager
	if len(config.Broker.CF.APIs) > 0 {
		l.Info("initializing CF connection manager with %d endpoint(s)", len(config.Broker.CF.APIs))
		cfManager = NewCFConnectionManager(config.Broker.CF.APIs, Logger.Wrap("cf-manager"))
		l.Info("CF connection manager initialized successfully")
	} else {
		l.Info("CF reconciliation disabled: no CF API endpoints configured")
	}

	// Initialize the deployment reconciler
	reconciler := NewReconcilerAdapter(&config, broker, vault, boshDirector, cfManager)

	// Initialize the VM monitor
	vmMonitor := NewVMMonitor(vault, boshDirector, &config)

	// Create context for server and reconciler lifecycle
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start CF health check loop if CF manager is initialized
	if cfManager != nil {
		cfManager.StartHealthCheckLoop(ctx)
		l.Info("CF health check loop started")
	}

	// Start the reconciler
	if err := reconciler.Start(ctx); err != nil {
		l.Error("Failed to start deployment reconciler: %s", err)
		// Non-fatal error - continue without reconciler
	} else {
		l.Info("Deployment reconciler started successfully")
		// Ensure reconciler is stopped on shutdown
		defer func() {
			if err := reconciler.Stop(); err != nil {
				l.Error("Error stopping reconciler: %s", err)
			}
		}()
	}

	// Start the VM monitor
	if err := vmMonitor.Start(); err != nil {
		l.Error("Failed to start VM monitor: %s", err)
		// Non-fatal error - continue without VM monitoring
	} else {
		l.Info("VM monitor started successfully")
		// Ensure VM monitor is stopped on shutdown
		defer func() {
			if err := vmMonitor.Stop(); err != nil {
				l.Error("Error stopping VM monitor: %s", err)
			}
		}()
	}

	// Initialize SSH service
	sshConfig := ssh.Config{
		Timeout:               time.Duration(config.BOSH.SSH.Timeout) * time.Second,
		ConnectTimeout:        time.Duration(config.BOSH.SSH.ConnectTimeout) * time.Second,
		SessionInitTimeout:    time.Duration(config.BOSH.SSH.SessionInitTimeout) * time.Second,
		OutputReadTimeout:     time.Duration(config.BOSH.SSH.OutputReadTimeout) * time.Second,
		MaxConcurrent:         config.BOSH.SSH.MaxConcurrent,
		MaxOutputSize:         config.BOSH.SSH.MaxOutputSize,
		KeepAlive:             time.Duration(config.BOSH.SSH.KeepAlive) * time.Second,
		RetryAttempts:         config.BOSH.SSH.RetryAttempts,
		RetryDelay:            time.Duration(config.BOSH.SSH.RetryDelay) * time.Second,
		InsecureIgnoreHostKey: config.BOSH.SSH.InsecureIgnoreHostKey,
		KnownHostsFile:        config.BOSH.SSH.KnownHostsFile,
	}

	// Set default values if not configured
	if sshConfig.Timeout == 0 {
		sshConfig.Timeout = 10 * time.Minute // Default to 10 minutes instead of 30 seconds
	}
	if sshConfig.ConnectTimeout == 0 {
		sshConfig.ConnectTimeout = 30 * time.Second // Increased from 10s to 30s
	}
	if sshConfig.SessionInitTimeout == 0 {
		sshConfig.SessionInitTimeout = 60 * time.Second // Default 60 seconds for session initialization
	}
	if sshConfig.OutputReadTimeout == 0 {
		sshConfig.OutputReadTimeout = 2 * time.Second // Default 2 seconds for output reading
	}
	if sshConfig.MaxConcurrent == 0 {
		sshConfig.MaxConcurrent = 10
	}
	if sshConfig.MaxOutputSize == 0 {
		sshConfig.MaxOutputSize = 1024 * 1024 // 1MB
	}
	if sshConfig.KeepAlive == 0 {
		sshConfig.KeepAlive = 10 * time.Second
	}
	if sshConfig.RetryAttempts == 0 {
		sshConfig.RetryAttempts = 3
	}
	if sshConfig.RetryDelay == 0 {
		sshConfig.RetryDelay = 5 * time.Second
	}

	// Set security defaults - secure by default, with BOSH director auto-discovery
	if sshConfig.KnownHostsFile == "" {
		sshConfig.KnownHostsFile = "/home/vcap/.ssh/known_hosts"
	}

	l.Info("Creating SSH service with timeout=%v, maxConcurrent=%d", sshConfig.Timeout, sshConfig.MaxConcurrent)
	sshService := ssh.NewSSHService(broker.BOSH, sshConfig, Logger.Wrap("ssh"))

	// SSH security configuration
	if config.BOSH.SSH.InsecureIgnoreHostKey {
		l.Info("SSH security: Using insecure host key verification (not recommended for production)")
	} else {
		l.Info("SSH security: Using known_hosts file at %s with auto-discovery", sshConfig.KnownHostsFile)
		l.Info("SSH host keys will be automatically added to known_hosts on first connection")
	}

	// Ensure SSH service is closed on shutdown
	defer func() {
		if err := sshService.Close(); err != nil {
			l.Error("Error closing SSH service: %s", err)
		}
	}()

	// Create RabbitMQ SSH service
	l.Info("Creating RabbitMQ SSH service")
	rabbitmqSSHService := rabbitmq.NewRabbitMQSSHService(sshService, Logger.Wrap("rabbitmq-ssh"))

	// Create RabbitMQ metadata service
	l.Info("Creating RabbitMQ metadata service")
	rabbitmqMetadataService := rabbitmq.NewMetadataService(Logger.Wrap("rabbitmq-metadata"))

	// Create RabbitMQ executor service
	l.Info("Creating RabbitMQ executor service")
	rabbitmqExecutorService := rabbitmq.NewExecutorService(rabbitmqSSHService, rabbitmqMetadataService, Logger.Wrap("rabbitmq-executor"))

	// Create RabbitMQ audit service
	l.Info("Creating RabbitMQ audit service")
	vaultAPIClient, err := vault.GetAPIClient()
	if err != nil {
		l.Error("Failed to get Vault API client: %s", err)
		vaultAPIClient = nil // Allow service to handle nil client gracefully
	}
	rabbitmqAuditService := rabbitmq.NewAuditService(vaultAPIClient, Logger.Wrap("rabbitmq-audit"))

	// Create RabbitMQ plugins metadata service
	l.Info("Creating RabbitMQ plugins metadata service")
	rabbitmqPluginsMetadataService := rabbitmq.NewPluginsMetadataService(Logger.Wrap("rabbitmq-plugins-metadata"))

	// Create RabbitMQ plugins executor service
	l.Info("Creating RabbitMQ plugins executor service")
	rabbitmqPluginsExecutorService := rabbitmq.NewPluginsExecutorService(rabbitmqSSHService, rabbitmqPluginsMetadataService, Logger.Wrap("rabbitmq-plugins-executor"))

	// Create RabbitMQ plugins audit service
	l.Info("Creating RabbitMQ plugins audit service")
	rabbitmqPluginsAuditService := rabbitmq.NewPluginsAuditService(vaultAPIClient, Logger.Wrap("rabbitmq-plugins-audit"))

	// Create WebSocket SSH handler
	l.Info("Creating WebSocket SSH handler")
	wsConfig := websocket.Config{
		ReadBufferSize:    config.BOSH.SSH.WebSocket.ReadBufferSize,
		WriteBufferSize:   config.BOSH.SSH.WebSocket.WriteBufferSize,
		HandshakeTimeout:  time.Duration(config.BOSH.SSH.WebSocket.HandshakeTimeout) * time.Second,
		MaxMessageSize:    int64(config.BOSH.SSH.WebSocket.MaxMessageSize),
		PingInterval:      time.Duration(config.BOSH.SSH.WebSocket.PingInterval) * time.Second,
		PongTimeout:       time.Duration(config.BOSH.SSH.WebSocket.PongTimeout) * time.Second,
		MaxSessions:       config.BOSH.SSH.WebSocket.MaxSessions,
		SessionTimeout:    time.Duration(config.BOSH.SSH.WebSocket.SessionTimeout) * time.Second,
		EnableCompression: config.BOSH.SSH.WebSocket.EnableCompression,
	}

	// Set default WebSocket configuration values
	if wsConfig.ReadBufferSize == 0 {
		wsConfig.ReadBufferSize = 4096
	}
	if wsConfig.WriteBufferSize == 0 {
		wsConfig.WriteBufferSize = 4096
	}
	if wsConfig.HandshakeTimeout == 0 {
		wsConfig.HandshakeTimeout = 10 * time.Second
	}
	if wsConfig.MaxMessageSize == 0 {
		wsConfig.MaxMessageSize = 32 * 1024 // 32KB
	}
	if wsConfig.PingInterval == 0 {
		wsConfig.PingInterval = 30 * time.Second
	}
	if wsConfig.PongTimeout == 0 {
		wsConfig.PongTimeout = 10 * time.Second
	}
	if wsConfig.MaxSessions == 0 {
		wsConfig.MaxSessions = 50
	}
	if wsConfig.SessionTimeout == 0 {
		wsConfig.SessionTimeout = 30 * time.Minute
	}

	var webSocketHandler *websocket.SSHHandler
	if config.BOSH.SSH.WebSocket.Enabled != nil && *config.BOSH.SSH.WebSocket.Enabled {
		webSocketHandler = websocket.NewSSHHandler(sshService, wsConfig, Logger.Wrap("websocket-ssh"))
		l.Info("WebSocket SSH handler created successfully")

		// Ensure WebSocket handler is closed on shutdown
		defer func() {
			if err := webSocketHandler.Close(); err != nil {
				l.Error("Error closing WebSocket handler: %s", err)
			}
		}()
	} else {
		l.Info("WebSocket SSH is disabled in configuration")
	}

	// Create the main API handler
	apiHandler := &API{
		Username: config.Broker.Username,
		Password: config.Broker.Password,
		WebRoot:  ui,
		Internal: &InternalApi{
			Env:                            config.Env,
			Vault:                          vault,
			Broker:                         broker,
			Config:                         config,
			VMMonitor:                      vmMonitor,
			Services:                       services.NewManagerWithCFConfig(Logger.Wrap("services").Debug, config.Broker.CF.BrokerURL, config.Broker.CF.BrokerUser, config.Broker.CF.BrokerPass),
			CFManager:                      cfManager,
			SSHService:                     sshService,
			RabbitMQSSHService:             rabbitmqSSHService,
			RabbitMQMetadataService:        rabbitmqMetadataService,
			RabbitMQExecutorService:        rabbitmqExecutorService,
			RabbitMQAuditService:           rabbitmqAuditService,
			RabbitMQPluginsMetadataService: rabbitmqPluginsMetadataService,
			RabbitMQPluginsExecutorService: rabbitmqPluginsExecutorService,
			RabbitMQPluginsAuditService:    rabbitmqPluginsAuditService,
			WebSocketHandler:               webSocketHandler,
			SecurityMiddleware:             services.NewSecurityMiddleware(Logger.Wrap("security").Debug),
		},
		Primary: brokerapi.New(
			broker,
			lager.NewLogger("blacksmith-broker"),
			brokerapi.BrokerCredentials{
				Username: config.Broker.Username,
				Password: config.Broker.Password,
			},
		),
	}

	// Create HTTP server
	httpServer := startHTTPServer(&config, apiHandler, l)

	// Create HTTPS server if TLS is enabled
	httpsServer, err := startHTTPSServer(&config, apiHandler, l)
	if err != nil {
		l.Error("Failed to create HTTPS server: %s", err)
		os.Exit(2)
	}

	// Start servers
	var wg sync.WaitGroup

	// Start HTTP server
	wg.Add(1)
	go func() {
		defer wg.Done()
		l.Info("Starting HTTP server...")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			l.Error("HTTP server failed: %s", err)
			cancel()
		}
	}()

	// Start HTTPS server if enabled
	if httpsServer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.Info("Starting HTTPS server...")
			if err := httpsServer.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
				l.Error("HTTPS server failed: %s", err)
				cancel()
			}
		}()
	}

	// Start BOSH maintenance loop
	wg.Add(1)
	go func() {
		defer wg.Done()
		boshMaintenanceLoop := time.NewTicker(1 * time.Hour)
		defer boshMaintenanceLoop.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-boshMaintenanceLoop.C:
				vaultDB, err := vault.getVaultDB()
				if err != nil {
					l.Error("error grabbing vaultdb for debugging: %s", err)
				}
				if jsonData, err := json.Marshal(vaultDB.Data); err != nil {
					l.Debug("current vault db looks like: %v (json marshal error: %s)", vaultDB.Data, err)
				} else {
					l.Debug("current vault db looks like: %s", string(jsonData))
				}
				if _, err := broker.serviceWithNoDeploymentCheck(); err != nil {
					l.Error("service with no deployment check failed: %s", err)
				}
			}
		}
	}()

	// Wait for shutdown signal
	select {
	case sig := <-sigChan:
		l.Info("Received signal %s, shutting down gracefully...", sig)
	case <-ctx.Done():
		l.Info("Context cancelled, shutting down...")
	}

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		l.Error("Error shutting down HTTP server: %s", err)
	}

	if httpsServer != nil {
		if err := httpsServer.Shutdown(shutdownCtx); err != nil {
			l.Error("Error shutting down HTTPS server: %s", err)
		}
	}

	cancel()  // Cancel context to stop other goroutines
	wg.Wait() // Wait for all goroutines to finish

	l.Info("Blacksmith service broker shut down complete")
}
