package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
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
	"blacksmith/internal/api"
	loggerPkg "blacksmith/pkg/logger"
	"blacksmith/pkg/services"
	"blacksmith/services/rabbitmq"
	"blacksmith/shield"
	"blacksmith/websocket"
	"code.cloudfoundry.org/lager"
	"github.com/pivotal-cf/brokerapi/v8"
)

// Configuration default constants.
const (
	// Exit code for errors
	exitCodeError = 2

	// SSH service defaults.
	DefaultSSHTimeout        = 10 * time.Minute
	DefaultSSHConnectTimeout = 30 * time.Second
	DefaultSSHInitTimeout    = 60 * time.Second
	DefaultSSHOutputTimeout  = 2 * time.Second
	DefaultSSHMaxConcurrent  = 10
	DefaultSSHMaxOutputSize  = 1024 * 1024 // 1MB
	DefaultSSHKeepAlive      = 10 * time.Second
	DefaultSSHRetryDelay     = 5 * time.Second

	// WebSocket configuration defaults.
	DefaultWSHandshakeTimeout = 10 * time.Second
	DefaultWSMaxMessageSize   = 32 * 1024 // 32KB
	DefaultWSSessionTimeout   = 30 * time.Minute
	DefaultWSCheckInterval    = 15 * time.Second

	// Application defaults.
	DefaultShutdownTimeout = 30 * time.Second
	DefaultMinArgsRequired = 3

	// Vault defaults.
	DefaultVaultTimeout        = 30 * time.Second
	DefaultVaultHistoryLimit   = 50
	DefaultVaultUnsealInterval = 15 * time.Second
	
	// Additional WebSocket defaults  
	DefaultWSPingInterval      = 5 * time.Second
	DefaultWSPongTimeout       = 10 * time.Second
)

// BuildInfo holds build-time information.
type BuildInfo struct {
	Version   string
	BuildTime string
	GitCommit string
}

// These variables get set via ldflags during build
//
//nolint:gochecknoglobals // Set via ldflags at build time
var (
	version   = "(development version)"
	buildTime = "unknown"
	gitCommit = "unknown"
)

// GetBuildInfo returns the build information.
func GetBuildInfo() *BuildInfo {
	return &BuildInfo{
		Version:   version,
		BuildTime: buildTime,
		GitCommit: gitCommit,
	}
}

// createHTTPSRedirectHandler creates an HTTP handler that redirects all requests to HTTPS.
func createHTTPSRedirectHandler(httpsPort string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if strings.Contains(host, ":") {
			host = strings.Split(host, ":")[0]
		}

		redirectURL := fmt.Sprintf("https://%s%s", net.JoinHostPort(host, httpsPort), r.RequestURI)
		http.Redirect(w, r, redirectURL, http.StatusMovedPermanently)
	}
}

// startHTTPServer starts an HTTP server, either for redirects (when TLS enabled) or normal operation.
func startHTTPServer(config *Config, handler http.Handler, logger loggerPkg.Logger) *http.Server {
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
		logger.Info("HTTP server on %s will redirect to HTTPS port %s", bind, config.Broker.TLS.Port)
	} else {
		// When TLS is disabled, HTTP server handles normal traffic
		// Apply compression middleware if enabled
		compressionConfig := config.Broker.Compression
		if compressionConfig.Enabled {
			httpHandler = NewCompressionMiddleware(handler, compressionConfig)
			logger.Info("HTTP server compression enabled with types: %v", compressionConfig.Types)
		} else {
			httpHandler = handler
		}

		logger.Info("HTTP server will listen on %s", bind)
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

// startHTTPSServer starts an HTTPS server with TLS configuration.
func startHTTPSServer(config *Config, handler http.Handler, logger loggerPkg.Logger) (*http.Server, error) {
	if !config.Broker.TLS.Enabled {
		return nil, nil
	}

	tlsConfig, err := CreateTLSConfig(config.Broker.TLS)
	if err != nil {
		return nil, fmt.Errorf("failed to create TLS configuration: %w", err)
	}

	bind := fmt.Sprintf("%s:%s", config.Broker.BindIP, config.Broker.TLS.Port)
	logger.Info("HTTPS server will listen on %s", bind)

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
		logger.Info("HTTPS server compression enabled with types: %v", compressionConfig.Types)
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

	// Important: Disable HTTP/2 to ensure WebSocket (HTTP/1.1 upgrade) works reliably with browsers
	// that do not support RFC8441 (Extended CONNECT over HTTP/2). With HTTP/2 enabled, browsers may
	// negotiate h2 and attempt a GET upgrade on an HTTP/2 stream, which cannot be hijacked.
	server.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))

	logger.Info("HTTPS server configured with HTTP/2 disabled for WebSockets; address: %s", bind)

	return server, nil
}

// parseFlags handles command-line flag parsing and version display.
func parseFlags(buildInfo *BuildInfo) string {
	showVersion := flag.Bool("v", false, "Display the version of Blacksmith")
	configPath := flag.String("c", "", "path to config")
	flag.Parse()

	if *showVersion {
		fmt.Printf("blacksmith %s\n", buildInfo.Version)      //nolint:forbidigo // CLI version output
		fmt.Printf("  Build Time: %s\n", buildInfo.BuildTime) //nolint:forbidigo // CLI version output
		fmt.Printf("  Git Commit: %s\n", buildInfo.GitCommit) //nolint:forbidigo // CLI version output
		fmt.Printf("  Go Version: %s\n", runtime.Version())   //nolint:forbidigo // CLI version output
		os.Exit(0)
	}

	return *configPath
}

// initializeConfig loads and validates configuration.
func initializeConfig(configPath string, logger loggerPkg.Logger) *Config {
	config, err := ReadConfig(configPath)
	if err != nil {
		log.Fatal(err)
	}

	if config.Debug {
		logger.SetLevel("debug")
	}

	// TLS configuration validation
	if config.Broker.TLS.Enabled {
		err := ValidateCertificateFiles(config.Broker.TLS.Certificate, config.Broker.TLS.Key)
		if err != nil {
			logger.Error("TLS configuration error: %s", err)
			os.Exit(exitCodeError)
		}

		logger.Info("TLS enabled - certificate: %s, key: %s", config.Broker.TLS.Certificate, config.Broker.TLS.Key)
	}

	return &config
}

// initializeVault sets up and initializes Vault client.
func initializeVault(config *Config, logger loggerPkg.Logger) *Vault {
	vault := &Vault{
		URL:      config.Vault.Address,
		Token:    "", // will be supplied soon.
		Insecure: config.Vault.Insecure,
	}

	// Configure vault auto-unseal behavior
	vault.autoUnsealEnabled = config.Vault.AutoUnseal
	if config.Vault.UnsealCooldown != "" {
		if d, err := time.ParseDuration(config.Vault.UnsealCooldown); err == nil {
			vault.unsealCooldown = d
		}
	}

	err := vault.WaitForVaultReady()
	if err != nil {
		logger.Error("Vault readiness check failed: %s", err)
		log.Fatal(err)
	}

	err = vault.Init(config.Vault.CredPath)
	if err != nil {
		log.Fatal(err)
	}

	err = vault.VerifyMount("secret", true)
	if err != nil {
		log.Fatal(err)
	}

	// Store blacksmith plans to Vault after ensuring KVv2
	planStorage := NewPlanStorage(vault, config)

	planStorage.StorePlans(context.Background())
	// Don't fail startup if plan storage fails

	return vault
}

// initializeBOSH creates and configures BOSH director.
func initializeBOSH(config *Config, logger loggerPkg.Logger) *bosh.PooledDirector {
	// Set BOSH environment variables for CLI compatibility
	if err := os.Setenv("BOSH_CLIENT", config.BOSH.Username); err != nil {
		logger.Error("Failed to set BOSH_CLIENT env var: %s", err)
	}

	if err := os.Setenv("BOSH_CLIENT_SECRET", config.BOSH.Password); err != nil {
		logger.Error("Failed to set BOSH_CLIENT_SECRET env var: %s", err)
	}

	logger.Debug("Set BOSH_CLIENT to: %s", config.BOSH.Username)

	// Use logger for BOSH operations
	boshLogger := logger

	boshDirector, err := bosh.CreatePooledDirector(
		config.BOSH.Address,
		config.BOSH.Username,
		config.BOSH.Password,
		config.BOSH.CACert,
		config.BOSH.SkipSslValidation,
		config.BOSH.MaxConnections,
		time.Duration(config.BOSH.ConnectionTimeout)*time.Second,
		boshLogger,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to authenticate to BOSH: %s\n", err)
		os.Exit(exitCodeError)
	}

	logger.Info("BOSH director initialized with connection pooling (max: %d connections, timeout: %ds)",
		config.BOSH.MaxConnections, config.BOSH.ConnectionTimeout)

	return boshDirector
}

// updateBOSHCloudConfig updates BOSH cloud config if provided.
func updateBOSHCloudConfig(config *Config, boshDirector bosh.Director, logger loggerPkg.Logger) {
	if config.BOSH.CloudConfig == "" {
		return
	}

	// Check if cloud config is effectively empty (just {} or whitespace)
	trimmed := strings.TrimSpace(config.BOSH.CloudConfig)
	if trimmed == "{}" || trimmed == "---" || trimmed == "--- {}" || trimmed == "" {
		return
	}

	logger.Info("updating cloud-config...")
	logger.Debug("updating cloud-config with:\n%s", config.BOSH.CloudConfig)

	err := boshDirector.UpdateCloudConfig(config.BOSH.CloudConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to update CLOUD-CONFIG: %s\ncloud-config:\n%s\n", err, config.BOSH.CloudConfig)
		os.Exit(exitCodeError)
	}
}

// uploadBOSHReleases uploads configured BOSH releases.
func uploadBOSHReleases(config *Config, boshDirector bosh.Director, logger loggerPkg.Logger) {
	if config.BOSH.Releases == nil {
		return
	}

	releases, err := boshDirector.GetReleases()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to retrieve RELEASES list: %s\n", err)
		os.Exit(exitCodeError)
	}

	have := make(map[string]bool)

	for _, r := range releases {
		for _, v := range r.ReleaseVersions {
			have[r.Name+"/"+v.Version] = true
		}
	}

	logger.Info("uploading releases...")

	for _, release := range config.BOSH.Releases {
		if have[release.Name+"/"+release.Version] {
			logger.Info("skipping %s/%s (already uploaded)", release.Name, release.Version)

			continue
		}

		logger.Debug("uploading release %s/%s [sha1 %s] from %s", release.Name, release.Version, release.SHA1, release.URL)

		task, err := boshDirector.UploadRelease(release.URL, release.SHA1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nFailed to upload RELEASE (%s) sha1 [%s]: %s\n", release.URL, release.SHA1, err)
			os.Exit(exitCodeError)
		}

		logger.Info("uploading release %s/%s [sha1 %s] in BOSH task %d, from %s", release.Name, release.Version, release.SHA1, task.ID, release.URL)
	}
}

// uploadBOSHStemcells uploads configured BOSH stemcells.
func uploadBOSHStemcells(config *Config, boshDirector bosh.Director, logger loggerPkg.Logger) {
	if config.BOSH.Stemcells == nil {
		return
	}

	stemcells, err := boshDirector.GetStemcells()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to retrieve STEMCELLS list: %s\n", err)
		os.Exit(exitCodeError)
	}

	have := make(map[string]bool)
	for _, stemcell := range stemcells {
		have[stemcell.Name+"/"+stemcell.Version] = true
	}

	logger.Info("uploading stemcells...")

	for _, stemcell := range config.BOSH.Stemcells {
		if have[stemcell.Name+"/"+stemcell.Version] {
			logger.Info("skipping %s/%s (already uploaded)", stemcell.Name, stemcell.Version)

			continue
		}

		logger.Debug("uploading stemcell %s/%s [sha1 %s] from %s", stemcell.Name, stemcell.Version, stemcell.SHA1, stemcell.URL)

		task, err := boshDirector.UploadStemcell(stemcell.URL, stemcell.SHA1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nFailed to upload STEMCELL (%s) sha1 [%s]: %s\n", err, stemcell.URL, stemcell.SHA1)
			os.Exit(exitCodeError)
		}

		logger.Info("uploading stemcell %s/%s [sha1 %s] in BOSH task %d, from %s", stemcell.Name, stemcell.Version, stemcell.SHA1, task.ID, stemcell.URL)
	}
}

// initializeShieldClient creates SHIELD client if enabled
//
//nolint:ireturn // Returns interface to allow both NoopClient and ShieldClient implementations
func initializeShieldClient(config *Config, logger loggerPkg.Logger) shield.Client {
	var shieldClient shield.Client = &shield.NoopClient{}
	if !config.Shield.Enabled {
		return shieldClient
	}

	cfg := shield.Config{
		Address:          config.Shield.Address,
		Insecure:         config.Shield.Insecure,
		Agent:            config.Shield.Agent,
		Tenant:           config.Shield.Tenant,
		Store:            config.Shield.Store,
		Schedule:         config.Shield.Schedule,
		Retain:           config.Shield.Retain,
		EnabledOnTargets: config.Shield.EnabledOnTargets,
		Logger:           logger.Named("shield"),
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
		os.Exit(exitCodeError)
	}

	logger.Debug("creating S.H.I.E.L.D. client with config: %+v", cfg)

	var err error

	networkClient, err := shield.NewClient(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create S.H.I.E.L.D. client: %s\n", err)
		os.Exit(exitCodeError)
	}

	return networkClient
}

// initializeServices creates and configures SSH and RabbitMQ services.
func initializeServices(config *Config, broker *Broker, vault *Vault, logger loggerPkg.Logger) (*ssh.ServiceImpl, *rabbitmq.SSHService, *rabbitmq.MetadataService, *rabbitmq.ExecutorService, *rabbitmq.AuditService, *rabbitmq.PluginsMetadataService, *rabbitmq.PluginsExecutorService, *rabbitmq.PluginsAuditService, *websocket.SSHHandler) {
	// Initialize SSH service
	sshConfig := ssh.Config{
		Timeout:               time.Duration(config.SSH.Timeout) * time.Second,
		ConnectTimeout:        time.Duration(config.SSH.ConnectTimeout) * time.Second,
		SessionInitTimeout:    time.Duration(config.SSH.SessionInitTimeout) * time.Second,
		OutputReadTimeout:     time.Duration(config.SSH.OutputReadTimeout) * time.Second,
		MaxConcurrent:         config.SSH.MaxConcurrent,
		MaxOutputSize:         config.SSH.MaxOutputSize,
		KeepAlive:             time.Duration(config.SSH.KeepAlive) * time.Second,
		RetryAttempts:         config.SSH.RetryAttempts,
		RetryDelay:            time.Duration(config.SSH.RetryDelay) * time.Second,
		InsecureIgnoreHostKey: config.SSH.InsecureIgnoreHostKey,
		KnownHostsFile:        config.SSH.KnownHostsFile,
	}

	// Set default values if not configured
	setSSHServiceDefaults(&sshConfig)

	logger.Info("Creating SSH service with timeout=%v, maxConcurrent=%d", sshConfig.Timeout, sshConfig.MaxConcurrent)
	sshService := ssh.NewSSHService(broker.BOSH, sshConfig, logger.Named("ssh"))

	logSSHSecurity(config, sshConfig, logger)

	// Create RabbitMQ services
	rabbitmqSSHService, rabbitmqMetadataService, rabbitmqExecutorService, rabbitmqAuditService, rabbitmqPluginsMetadataService, rabbitmqPluginsExecutorService, rabbitmqPluginsAuditService := createRabbitMQServices(sshService, vault, logger)

	// Create WebSocket handler
	webSocketHandler := createWebSocketHandler(config, sshService, logger)

	return sshService, rabbitmqSSHService, rabbitmqMetadataService, rabbitmqExecutorService, rabbitmqAuditService, rabbitmqPluginsMetadataService, rabbitmqPluginsExecutorService, rabbitmqPluginsAuditService, webSocketHandler
}

// setSSHServiceDefaults sets default values for SSH service configuration.
func setSSHServiceDefaults(sshConfig *ssh.Config) {
	if sshConfig.Timeout == 0 {
		sshConfig.Timeout = DefaultSSHTimeout
	}

	if sshConfig.ConnectTimeout == 0 {
		sshConfig.ConnectTimeout = DefaultSSHConnectTimeout
	}

	if sshConfig.SessionInitTimeout == 0 {
		sshConfig.SessionInitTimeout = DefaultSSHInitTimeout
	}

	if sshConfig.OutputReadTimeout == 0 {
		sshConfig.OutputReadTimeout = DefaultSSHOutputTimeout
	}

	if sshConfig.MaxConcurrent == 0 {
		sshConfig.MaxConcurrent = 10
	}

	if sshConfig.MaxOutputSize == 0 {
		sshConfig.MaxOutputSize = DefaultSSHMaxOutputSize // 1MB
	}

	if sshConfig.KeepAlive == 0 {
		sshConfig.KeepAlive = DefaultSSHKeepAlive
	}

	if sshConfig.RetryAttempts == 0 {
		sshConfig.RetryAttempts = 3
	}

	if sshConfig.RetryDelay == 0 {
		sshConfig.RetryDelay = DefaultSSHRetryDelay
	}

	if sshConfig.KnownHostsFile == "" {
		sshConfig.KnownHostsFile = "/home/vcap/.ssh/known_hosts"
	}
}

// logSSHSecurity logs SSH security configuration.
func logSSHSecurity(config *Config, sshConfig ssh.Config, logger loggerPkg.Logger) {
	if config.SSH.InsecureIgnoreHostKey {
		logger.Info("SSH security: Using insecure host key verification (not recommended for production)")
	} else {
		logger.Info("SSH security: Using known_hosts file at %s with auto-discovery", sshConfig.KnownHostsFile)
		logger.Info("SSH host keys will be automatically added to known_hosts on first connection")
	}
}

// createRabbitMQServices creates all RabbitMQ-related services.
func createRabbitMQServices(sshService ssh.SSHService, vault *Vault, logger loggerPkg.Logger) (*rabbitmq.SSHService, *rabbitmq.MetadataService, *rabbitmq.ExecutorService, *rabbitmq.AuditService, *rabbitmq.PluginsMetadataService, *rabbitmq.PluginsExecutorService, *rabbitmq.PluginsAuditService) {
	logger.Info("Creating RabbitMQ SSH service")

	rabbitmqSSHService := rabbitmq.NewRabbitMQSSHService(sshService, logger.Named("rabbitmq-ssh"))

	logger.Info("Creating RabbitMQ metadata service")

	rabbitmqMetadataService := rabbitmq.NewMetadataService(logger.Named("rabbitmq-metadata"))

	logger.Info("Creating RabbitMQ executor service")

	rabbitmqExecutorService := rabbitmq.NewExecutorService(rabbitmqSSHService, rabbitmqMetadataService, logger.Named("rabbitmq-executor"))

	logger.Info("Creating RabbitMQ audit service")

	vaultAPIClient, err := vault.GetAPIClient()
	if err != nil {
		logger.Error("Failed to get Vault API client: %s", err)

		vaultAPIClient = nil // Allow service to handle nil client gracefully
	}

	rabbitmqAuditService := rabbitmq.NewAuditService(vaultAPIClient, logger.Named("rabbitmq-audit"))

	logger.Info("Creating RabbitMQ plugins metadata service")

	rabbitmqPluginsMetadataService := rabbitmq.NewPluginsMetadataService(logger.Named("rabbitmq-plugins-metadata"))

	logger.Info("Creating RabbitMQ plugins executor service")

	rabbitmqPluginsExecutorService := rabbitmq.NewPluginsExecutorService(rabbitmqSSHService, rabbitmqPluginsMetadataService, logger.Named("rabbitmq-plugins-executor"))

	logger.Info("Creating RabbitMQ plugins audit service")

	rabbitmqPluginsAuditService := rabbitmq.NewPluginsAuditService(vaultAPIClient, logger.Named("rabbitmq-plugins-audit"))

	return rabbitmqSSHService, rabbitmqMetadataService, rabbitmqExecutorService, rabbitmqAuditService, rabbitmqPluginsMetadataService, rabbitmqPluginsExecutorService, rabbitmqPluginsAuditService
}

// createWebSocketHandler creates WebSocket SSH handler if enabled.
func createWebSocketHandler(config *Config, sshService ssh.SSHService, logger loggerPkg.Logger) *websocket.SSHHandler {
	if config.SSH.WebSocket.Enabled == nil || !*config.SSH.WebSocket.Enabled {
		logger.Info("WebSocket SSH is disabled in configuration")

		return nil
	}

	logger.Info("Creating WebSocket SSH handler")

	wsConfig := websocket.Config{
		ReadBufferSize:    config.SSH.WebSocket.ReadBufferSize,
		WriteBufferSize:   config.SSH.WebSocket.WriteBufferSize,
		HandshakeTimeout:  time.Duration(config.SSH.WebSocket.HandshakeTimeout) * time.Second,
		MaxMessageSize:    int64(config.SSH.WebSocket.MaxMessageSize),
		PingInterval:      time.Duration(config.SSH.WebSocket.PingInterval) * time.Second,
		PongTimeout:       time.Duration(config.SSH.WebSocket.PongTimeout) * time.Second,
		MaxSessions:       config.SSH.WebSocket.MaxSessions,
		SessionTimeout:    time.Duration(config.SSH.WebSocket.SessionTimeout) * time.Second,
		EnableCompression: config.SSH.WebSocket.EnableCompression,
	}

	// Set default WebSocket configuration values
	setWebSocketDefaults(&wsConfig)

	webSocketHandler := websocket.NewSSHHandler(sshService, wsConfig, logger.Named("websocket-ssh"))

	logger.Info("WebSocket SSH handler created successfully")

	return webSocketHandler
}

// setWebSocketDefaults sets default values for WebSocket configuration.
func setWebSocketDefaults(wsConfig *websocket.Config) {
	if wsConfig.ReadBufferSize == 0 {
		wsConfig.ReadBufferSize = 4096
	}

	if wsConfig.WriteBufferSize == 0 {
		wsConfig.WriteBufferSize = 4096
	}

	if wsConfig.HandshakeTimeout == 0 {
		wsConfig.HandshakeTimeout = DefaultWSHandshakeTimeout
	}

	if wsConfig.MaxMessageSize == 0 {
		wsConfig.MaxMessageSize = DefaultWSMaxMessageSize // 32KB
	}

	if wsConfig.PingInterval == 0 {
		wsConfig.PingInterval = DefaultWSPingInterval
	}

	if wsConfig.PongTimeout == 0 {
		wsConfig.PongTimeout = DefaultWSPongTimeout
	}

	if wsConfig.MaxSessions == 0 {
		wsConfig.MaxSessions = 50
	}

	if wsConfig.SessionTimeout == 0 {
		wsConfig.SessionTimeout = DefaultWSSessionTimeout
	}
}

// startBackgroundServices starts vault watcher, CF manager, reconciler and VM monitor.
func startBackgroundServices(config *Config, vault *Vault, cfManager *CFConnectionManager, reconciler *ReconcilerAdapter, vmMonitor *VMMonitor, ctx context.Context, logger loggerPkg.Logger) {
	// Start Vault health watcher to auto-unseal if Vault restarts and comes back sealed
	if config.Vault.AutoUnseal {
		interval := DefaultWSCheckInterval

		if config.Vault.HealthCheckInterval != "" {
			if d, err := time.ParseDuration(config.Vault.HealthCheckInterval); err == nil {
				interval = d
			}
		}

		go vault.StartHealthWatcher(ctx, interval)
	}

	// Start CF health check loop if CF manager is initialized
	if cfManager != nil {
		cfManager.StartHealthCheckLoop(ctx)
		logger.Info("CF health check loop started")
	}

	// Start the reconciler
	err := reconciler.Start(ctx)
	if err != nil {
		logger.Error("Failed to start deployment reconciler: %s", err)
		// Non-fatal error - continue without reconciler
	} else {
		logger.Info("Deployment reconciler started successfully")
		// Ensure reconciler is stopped on shutdown
		defer func() {
			err := reconciler.Stop()
			if err != nil {
				logger.Error("Error stopping reconciler: %s", err)
			}
		}()
	}

	// Start the VM monitor
	err = vmMonitor.Start(ctx)
	if err != nil {
		logger.Error("Failed to start VM monitor: %s", err)
		// Non-fatal error - continue without VM monitoring
	} else {
		logger.Info("VM monitor started successfully")
		// Ensure VM monitor is stopped on shutdown
		defer func() {
			vmMonitor.Stop()
		}()
	}
}

// runServersAndMaintenance starts HTTP/HTTPS servers and BOSH maintenance loop.
func runServersAndMaintenance(config *Config, apiHandler *API, broker *Broker, vault *Vault, ctx context.Context, cancel context.CancelFunc, logger loggerPkg.Logger) *sync.WaitGroup {
	// Create HTTP server
	httpServer := startHTTPServer(config, apiHandler, logger)

	// Create HTTPS server if TLS is enabled
	httpsServer, err := startHTTPSServer(config, apiHandler, logger)
	if err != nil {
		logger.Error("Failed to create HTTPS server: %s", err)
		os.Exit(exitCodeError)
	}

	// Start servers
	var waitGroup sync.WaitGroup

	// Start HTTP server
	waitGroup.Add(1)

	go func() {
		defer waitGroup.Done()

		logger.Info("Starting HTTP server...")

		err := httpServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed: %s", err)
			cancel()
		}
	}()

	// Start HTTPS server if enabled
	if httpsServer != nil {
		waitGroup.Add(1)

		go func() {
			defer waitGroup.Done()

			logger.Info("Starting HTTPS server...")

			err := httpsServer.ListenAndServeTLS("", "")
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				logger.Error("HTTPS server failed: %s", err)
				cancel()
			}
		}()
	}

	// Start BOSH maintenance loop
	waitGroup.Add(1)

	go func() {
		defer waitGroup.Done()

		boshMaintenanceLoop := time.NewTicker(1 * time.Hour)
		defer boshMaintenanceLoop.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-boshMaintenanceLoop.C:
				vaultDB, err := vault.getVaultDB(ctx)
				if err != nil {
					logger.Error("error grabbing vaultdb for debugging: %s", err)
				}

				if jsonData, err := json.Marshal(vaultDB.Data); err != nil {
					logger.Debug("current vault db looks like: %v (json marshal error: %s)", vaultDB.Data, err)
				} else {
					logger.Debug("current vault db looks like: %s", string(jsonData))
				}

				if _, err := broker.serviceWithNoDeploymentCheck(ctx); err != nil {
					logger.Error("service with no deployment check failed: %s", err)
				}
			}
		}
	}()

	// Setup graceful shutdown goroutine
	go func() {
		<-ctx.Done()
		logger.Info("Shutting down servers...")

		// Graceful shutdown
		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, DefaultShutdownTimeout)
		defer shutdownCancel()

		err := httpServer.Shutdown(shutdownCtx)
		if err != nil {
			logger.Error("Error shutting down HTTP server: %s", err)
		}

		if httpsServer != nil {
			err := httpsServer.Shutdown(shutdownCtx)
			if err != nil {
				logger.Error("Error shutting down HTTPS server: %s", err)
			}
		}
	}()

	return &waitGroup
}

func main() {
	// Initialize build info
	buildInfo := GetBuildInfo()

	// Parse command line flags
	configPath := parseFlags(buildInfo)

	// Initialize centralized logger from environment
	if err := loggerPkg.InitFromEnv(); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}

	// Get the global logger instance
	logger := loggerPkg.Get()

	// Log build version information at startup
	logger.Info("blacksmith starting - version: %s, build: %s, commit: %s, go: %s",
		buildInfo.Version, buildInfo.BuildTime, buildInfo.GitCommit, runtime.Version())

	config := initializeConfig(configPath, logger)
	vault := initializeVault(config, logger)
	boshDirector := initializeBOSH(config, logger)

	updateBOSHCloudConfig(config, boshDirector, logger)
	uploadBOSHReleases(config, boshDirector, logger)
	uploadBOSHStemcells(config, boshDirector, logger)

	shieldClient := initializeShieldClient(config, logger)

	broker := &Broker{
		Vault:  vault,
		BOSH:   boshDirector,
		Shield: shieldClient,
		Config: config,
	}

	// Read services from CLI args or auto-scan
	var err error

	if len(os.Args) > DefaultMinArgsRequired {
		logger.Info("reading services from CLI arguments: %s", strings.Join(os.Args[3:], ", "))
		err = broker.ReadServices(os.Args[3:]...)
	} else {
		logger.Info("no CLI arguments provided, using configuration-based service discovery")

		err = broker.ReadServices()
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read SERVICE directories: %s\n", err)
		os.Exit(exitCodeError)
	}

	var uiHandler http.Handler = &NullHandler{}
	if config.WebRoot != "" {
		uiHandler = http.FileServer(http.Dir(config.WebRoot))
	}

	logger.Info("blacksmith service broker v%s starting up...", buildInfo.Version)

	// Setup signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Initialize CF connection manager
	var cfManager *CFConnectionManager

	if len(config.Broker.CF.APIs) > 0 {
		logger.Info("initializing CF connection manager with %d endpoint(s)", len(config.Broker.CF.APIs))
		cfManager = NewCFConnectionManager(config.Broker.CF.APIs, logger.Named("cf-manager"))

		logger.Info("CF connection manager initialized successfully")
	} else {
		logger.Info("CF reconciliation disabled: no CF API endpoints configured")
	}

	// Initialize the deployment reconciler
	reconciler := NewReconcilerAdapter(config, broker, vault, boshDirector, cfManager)

	// Initialize the VM monitor
	vmMonitor := NewVMMonitor(vault, boshDirector, config)

	// Create context for server and reconciler lifecycle
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start background services
	startBackgroundServices(config, vault, cfManager, reconciler, vmMonitor, ctx, logger)

	// Initialize SSH and RabbitMQ services
	sshService, rabbitmqSSHService, rabbitmqMetadataService, rabbitmqExecutorService, rabbitmqAuditService, rabbitmqPluginsMetadataService, rabbitmqPluginsExecutorService, rabbitmqPluginsAuditService, webSocketHandler := initializeServices(config, broker, vault, logger)

	// Ensure SSH service is closed on shutdown
	defer func() {
		err := sshService.Close()
		if err != nil {
			logger.Error("Error closing SSH service: %s", err)
		}
	}()

	// Ensure WebSocket handler is closed on shutdown
	if webSocketHandler != nil {
		defer func() {
			err := webSocketHandler.Close()
			if err != nil {
				logger.Error("Error closing WebSocket handler: %s", err)
			}
		}()
	}

	// Create the main API handler with refactored InternalAPI
	internalAPI := api.NewInternalAPI(api.Dependencies{
		Config:                         config,
		Logger:                         logger,
		Vault:                          vault,
		Broker:                         broker,
		ServicesManager:                services.NewManagerWithCFConfig(logger.Named("services").Debug, config.Broker.CF.BrokerURL, config.Broker.CF.BrokerUser, config.Broker.CF.BrokerPass),
		CFManager:                      cfManager,
		VMMonitor:                      vmMonitor,
		SSHService:                     sshService,
		RabbitMQSSHService:             rabbitmqSSHService,
		RabbitMQMetadataService:        rabbitmqMetadataService,
		RabbitMQExecutorService:        rabbitmqExecutorService,
		RabbitMQAuditService:           rabbitmqAuditService,
		RabbitMQPluginsMetadataService: rabbitmqPluginsMetadataService,
		RabbitMQPluginsExecutorService: rabbitmqPluginsExecutorService,
		RabbitMQPluginsAuditService:    rabbitmqPluginsAuditService,
		WebSocketHandler:               webSocketHandler,
		SecurityMiddleware:             services.NewSecurityMiddleware(logger.Named("security").Debug),
	})

	apiHandler := &API{
		Username: config.Broker.Username,
		Password: config.Broker.Password,
		WebRoot:  uiHandler,
		Logger:   logger,
		Internal: internalAPI,
		Primary: brokerapi.New(
			broker,
			lager.NewLogger("blacksmith-broker"),
			brokerapi.BrokerCredentials{
				Username: config.Broker.Username,
				Password: config.Broker.Password,
			},
		),
	}

	// Start servers and maintenance loops
	serverWaitGroup := runServersAndMaintenance(config, apiHandler, broker, vault, ctx, cancel, logger)

	// Wait for shutdown signal
	select {
	case sig := <-sigChan:
		logger.Info("Received signal %s, shutting down gracefully...", sig)
	case <-ctx.Done():
		logger.Info("Context cancelled, shutting down...")
	}

	cancel()               // Cancel context to stop other goroutines
	serverWaitGroup.Wait() // Wait for all goroutines to finish

	logger.Info("Blacksmith service broker shut down complete")
}

// Use SSH.WebSocket.PingInterval as the authoritative setting for WS ping tuning.
