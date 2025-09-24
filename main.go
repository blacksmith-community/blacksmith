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

	"blacksmith/internal/api"
	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/ssh"
	"blacksmith/internal/broker"
	internalCF "blacksmith/internal/cf"
	"blacksmith/internal/compression"
	"blacksmith/internal/config"
	"blacksmith/internal/planstore"
	"blacksmith/internal/recovery"
	"blacksmith/internal/services/rabbitmq"
	internalTLS "blacksmith/internal/tls"
	internalVault "blacksmith/internal/vault"
	"blacksmith/internal/vmmonitor"
	loggerPkg "blacksmith/pkg/logger"
	"blacksmith/pkg/services"
	"blacksmith/shield"
	"blacksmith/websocket"
	"code.cloudfoundry.org/lager"
	"github.com/pivotal-cf/brokerapi/v8"
)

// Configuration default constants.
const (
	// Exit code for errors.
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

	// Additional WebSocket defaults.
	DefaultWSPingInterval = 5 * time.Second
	DefaultWSPongTimeout  = 10 * time.Second
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
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		host := request.Host
		if strings.Contains(host, ":") {
			host = strings.Split(host, ":")[0]
		}

		redirectURL := fmt.Sprintf("https://%s%s", net.JoinHostPort(host, httpsPort), request.RequestURI)
		http.Redirect(responseWriter, request, redirectURL, http.StatusMovedPermanently)
	}
}

// startHTTPServer starts an HTTP server, either for redirects (when TLS enabled) or normal operation.
func startHTTPServer(cfg *config.Config, handler http.Handler, logger loggerPkg.Logger) *http.Server {
	bind := fmt.Sprintf("%s:%s", cfg.Broker.BindIP, cfg.Broker.Port)

	readTimeout := 120
	if cfg.Broker.ReadTimeout > 0 {
		readTimeout = cfg.Broker.ReadTimeout
	}

	writeTimeout := 120
	if cfg.Broker.WriteTimeout > 0 {
		writeTimeout = cfg.Broker.WriteTimeout
	}

	idleTimeout := 300
	if cfg.Broker.IdleTimeout > 0 {
		idleTimeout = cfg.Broker.IdleTimeout
	}

	var httpHandler http.Handler
	if cfg.Broker.TLS.Enabled {
		// When TLS is enabled, HTTP server only handles redirects
		httpHandler = createHTTPSRedirectHandler(cfg.Broker.TLS.Port)
		logger.Info("HTTP server on %s will redirect to HTTPS port %s", bind, cfg.Broker.TLS.Port)
	} else {
		// When TLS is disabled, HTTP server handles normal traffic
		// Apply compression middleware if enabled
		compressionConfig := cfg.Broker.Compression
		if compressionConfig.Enabled {
			// Convert cfg.CompressionConfig to compression.Config
			compCfg := compression.Config{
				Enabled:      compressionConfig.Enabled,
				Types:        compressionConfig.Types,
				Level:        compressionConfig.Level,
				MinSize:      compressionConfig.MinSize,
				ContentTypes: compressionConfig.ContentTypes,
			}
			httpHandler = compression.NewMiddleware(handler, compCfg)

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
func startHTTPSServer(cfg *config.Config, handler http.Handler, logger loggerPkg.Logger) (*http.Server, error) {
	if !cfg.Broker.TLS.Enabled {
		return nil, internalTLS.ErrTLSDisabled
	}

	tlsConfig, err := internalTLS.CreateTLSConfig(cfg.Broker.TLS)
	if err != nil {
		return nil, fmt.Errorf("failed to create TLS configuration: %w", err)
	}

	bind := fmt.Sprintf("%s:%s", cfg.Broker.BindIP, cfg.Broker.TLS.Port)
	logger.Info("HTTPS server will listen on %s", bind)

	readTimeout := 120
	if cfg.Broker.ReadTimeout > 0 {
		readTimeout = cfg.Broker.ReadTimeout
	}

	writeTimeout := 120
	if cfg.Broker.WriteTimeout > 0 {
		writeTimeout = cfg.Broker.WriteTimeout
	}

	idleTimeout := 300
	if cfg.Broker.IdleTimeout > 0 {
		idleTimeout = cfg.Broker.IdleTimeout
	}

	// Apply compression middleware if enabled
	var httpsHandler http.Handler

	compressionConfig := cfg.Broker.Compression
	if compressionConfig.Enabled {
		// Convert cfg.CompressionConfig to compression.Config
		compCfg := compression.Config{
			Enabled:      compressionConfig.Enabled,
			Types:        compressionConfig.Types,
			Level:        compressionConfig.Level,
			MinSize:      compressionConfig.MinSize,
			ContentTypes: compressionConfig.ContentTypes,
		}
		httpsHandler = compression.NewMiddleware(handler, compCfg)

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
func initializeConfig(configPath string, logger loggerPkg.Logger) *config.Config {
	cfg, err := config.ReadConfig(configPath)
	if err != nil {
		log.Fatal(err)
	}

	if cfg.Debug {
		err := logger.SetLevel("debug")
		if err != nil {
			log.Fatal(err)
		}
	}

	// TLS configuration validation
	if cfg.Broker.TLS.Enabled {
		err := internalTLS.ValidateCertificateFiles(cfg.Broker.TLS.Certificate, cfg.Broker.TLS.Key)
		if err != nil {
			logger.Error("TLS configuration error: %s", err)
			os.Exit(exitCodeError)
		}

		logger.Info("TLS enabled - certificate: %s, key: %s", cfg.Broker.TLS.Certificate, cfg.Broker.TLS.Key)
	}

	return &cfg
}

// initializeVault sets up and initializes Vault client.
func initializeVault(cfg *config.Config, logger loggerPkg.Logger) *internalVault.Vault {
	vault := internalVault.New(cfg.Vault.Address, "", cfg.Vault.Insecure)

	// Ensure we know where credentials live and pre-load any configured token.
	vault.SetCredentialsPath(cfg.Vault.CredPath)

	if cfg.Vault.Token != "" {
		vault.SetToken(cfg.Vault.Token)
	}

	// Configure vault auto-unseal behavior
	if cfg.Vault.AutoUnseal {
		vault.EnableAutoUnseal(cfg.Vault.CredPath)
	}

	// Attempt to reuse existing credentials on disk before touching Vault.
	if vault.Token == "" {
		if _, err := vault.LoadTokenFromCredentials(); err != nil {
			logger.Debug("Vault token not yet available from credentials file: %s", err)
		} else {
			logger.Debug("Loaded Vault token from credentials file")
		}
	}

	err := vault.WaitForVaultReady()
	if err != nil {
		logger.Error("Vault readiness check failed: %s", err)
		log.Fatal(err)
	}

	err = vault.Init(cfg.Vault.CredPath)
	if err != nil {
		log.Fatal(err)
	}

	if vault.Token == "" {
		if _, err := vault.LoadTokenFromCredentials(); err != nil {
			logger.Warn("Vault token unavailable after initialization: %s", err)
		} else {
			logger.Debug("Loaded Vault token from credentials file after initialization")
		}
	}

	err = vault.VerifyMount("secret", true)
	if err != nil {
		log.Fatal(err)
	}

	// Store blacksmith plans to Vault after ensuring KVv2
	planStorage := planstore.New(vault, cfg)

	planStorage.StorePlans(context.Background())
	// Don't fail startup if plan storage fails

	return vault
}

// initializeBOSH creates and configures BOSH director.
func initializeBOSH(cfg *config.Config, logger loggerPkg.Logger) *bosh.PooledDirector {
	// Set BOSH environment variables for CLI compatibility
	err := os.Setenv("BOSH_CLIENT", cfg.BOSH.Username)
	if err != nil {
		logger.Error("Failed to set BOSH_CLIENT env var: %s", err)
	}

	err = os.Setenv("BOSH_CLIENT_SECRET", cfg.BOSH.Password)
	if err != nil {
		logger.Error("Failed to set BOSH_CLIENT_SECRET env var: %s", err)
	}

	logger.Debug("Set BOSH_CLIENT to: %s", cfg.BOSH.Username)

	// Use logger for BOSH operations
	boshLogger := loggerPkg.Get().Named("bosh")

	boshDirector, err := bosh.CreatePooledDirector(
		cfg.BOSH.Address,
		cfg.BOSH.Username,
		cfg.BOSH.Password,
		cfg.BOSH.CACert,
		cfg.BOSH.SkipSslValidation,
		cfg.BOSH.MaxConnections,
		time.Duration(cfg.BOSH.ConnectionTimeout)*time.Second,
		boshLogger,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to authenticate to BOSH: %s\n", err)
		os.Exit(exitCodeError)
	}

	logger.Info("BOSH director initialized with connection pooling (max: %d connections, timeout: %ds)",
		cfg.BOSH.MaxConnections, cfg.BOSH.ConnectionTimeout)

	return boshDirector
}

// updateBOSHCloudConfig updates BOSH cloud config if provided.
func updateBOSHCloudConfig(cfg *config.Config, boshDirector bosh.Director, logger loggerPkg.Logger) {
	if cfg.BOSH.CloudConfig == "" {
		return
	}

	// Check if cloud config is effectively empty (just {} or whitespace)
	trimmed := strings.TrimSpace(cfg.BOSH.CloudConfig)
	if trimmed == "{}" || trimmed == "---" || trimmed == "--- {}" || trimmed == "" {
		return
	}

	logger.Info("updating cloud-cfg...")
	logger.Debug("updating cloud-config with:\n%s", cfg.BOSH.CloudConfig)

	err := boshDirector.UpdateCloudConfig(cfg.BOSH.CloudConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to update CLOUD-CONFIG: %s\ncloud-config:\n%s\n", err, cfg.BOSH.CloudConfig)
		os.Exit(exitCodeError)
	}
}

// uploadBOSHReleases uploads configured BOSH releases.
func uploadBOSHReleases(cfg *config.Config, boshDirector bosh.Director, logger loggerPkg.Logger) {
	if cfg.BOSH.Releases == nil {
		return
	}

	releases, err := boshDirector.GetReleases()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to retrieve RELEASES list: %s\n", err)
		os.Exit(exitCodeError)
	}

	have := make(map[string]bool)

	for _, release := range releases {
		for _, version := range release.ReleaseVersions {
			have[release.Name+"/"+version.Version] = true
		}
	}

	logger.Info("uploading releases...")

	for _, release := range cfg.BOSH.Releases {
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
func uploadBOSHStemcells(cfg *config.Config, boshDirector bosh.Director, logger loggerPkg.Logger) {
	if cfg.BOSH.Stemcells == nil {
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

	for _, stemcell := range cfg.BOSH.Stemcells {
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
func initializeShieldClient(cfg *config.Config, logger loggerPkg.Logger) shield.Client {
	var shieldClient shield.Client = &shield.NoopClient{}
	if !cfg.Shield.Enabled {
		return shieldClient
	}

	shieldCfg := shield.Config{
		Address:          cfg.Shield.Address,
		Insecure:         cfg.Shield.Insecure,
		Agent:            cfg.Shield.Agent,
		Tenant:           cfg.Shield.Tenant,
		Store:            cfg.Shield.Store,
		Schedule:         cfg.Shield.Schedule,
		Retain:           cfg.Shield.Retain,
		EnabledOnTargets: cfg.Shield.EnabledOnTargets,
		Logger:           logger.Named("shield"),
	}

	if shieldCfg.Schedule == "" {
		shieldCfg.Schedule = "daily 6am"
	}

	if shieldCfg.Retain == "" {
		shieldCfg.Retain = "7d"
	}

	switch cfg.Shield.AuthMethod {
	case "local":
		shieldCfg.Authentication = &shield.LocalAuth{Username: cfg.Shield.Username, Password: cfg.Shield.Password}
	case "token":
		shieldCfg.Authentication = &shield.TokenAuth{Token: cfg.Shield.Token}
	default:
		fmt.Fprintf(os.Stderr, "Invalid S.H.I.E.L.D. authentication method (must be one of 'local' or 'token'): %s\n", cfg.Shield.AuthMethod)
		os.Exit(exitCodeError)
	}

	logger.Debug("creating S.H.I.E.L.D. client with config: %+v", shieldCfg)

	var err error

	networkClient, err := shield.NewClient(shieldCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create S.H.I.E.L.D. client: %s\n", err)
		os.Exit(exitCodeError)
	}

	return networkClient
}

// initializeServices creates and configures SSH and RabbitMQ services.
func initializeServices(cfg *config.Config, brokerInstance *broker.Broker, vault *internalVault.Vault, logger loggerPkg.Logger) (*ssh.ServiceImpl, *rabbitmq.SSHService, *rabbitmq.MetadataService, *rabbitmq.ExecutorService, *rabbitmq.AuditService, *rabbitmq.PluginsMetadataService, *rabbitmq.PluginsExecutorService, *rabbitmq.PluginsAuditService, *websocket.SSHHandler) {
	// Initialize SSH service
	sshConfig := ssh.Config{
		Timeout:               time.Duration(cfg.SSH.Timeout) * time.Second,
		ConnectTimeout:        time.Duration(cfg.SSH.ConnectTimeout) * time.Second,
		SessionInitTimeout:    time.Duration(cfg.SSH.SessionInitTimeout) * time.Second,
		OutputReadTimeout:     time.Duration(cfg.SSH.OutputReadTimeout) * time.Second,
		MaxConcurrent:         cfg.SSH.MaxConcurrent,
		MaxOutputSize:         cfg.SSH.MaxOutputSize,
		KeepAlive:             time.Duration(cfg.SSH.KeepAlive) * time.Second,
		RetryAttempts:         cfg.SSH.RetryAttempts,
		RetryDelay:            time.Duration(cfg.SSH.RetryDelay) * time.Second,
		InsecureIgnoreHostKey: cfg.SSH.InsecureIgnoreHostKey,
		KnownHostsFile:        cfg.SSH.KnownHostsFile,
	}

	// Set default values if not configured
	setSSHServiceDefaults(&sshConfig)

	logger.Info("Creating SSH service with timeout=%v, maxConcurrent=%d", sshConfig.Timeout, sshConfig.MaxConcurrent)
	sshService := ssh.NewSSHService(brokerInstance.BOSH, sshConfig, logger.Named("ssh"))

	logSSHSecurity(cfg, sshConfig, logger)

	// Create RabbitMQ services
	rabbitmqSSHService, rabbitmqMetadataService, rabbitmqExecutorService, rabbitmqAuditService, rabbitmqPluginsMetadataService, rabbitmqPluginsExecutorService, rabbitmqPluginsAuditService := createRabbitMQServices(sshService, vault, logger)

	// Create WebSocket handler
	webSocketHandler := createWebSocketHandler(cfg, sshService, logger)

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
func logSSHSecurity(cfg *config.Config, sshConfig ssh.Config, logger loggerPkg.Logger) {
	if cfg.SSH.InsecureIgnoreHostKey {
		logger.Info("SSH security: Using insecure host key verification (not recommended for production)")
	} else {
		logger.Info("SSH security: Using known_hosts file at %s with auto-discovery", sshConfig.KnownHostsFile)
		logger.Info("SSH host keys will be automatically added to known_hosts on first connection")
	}
}

// createRabbitMQServices creates all RabbitMQ-related services.
func createRabbitMQServices(sshService ssh.SSHService, vault *internalVault.Vault, logger loggerPkg.Logger) (*rabbitmq.SSHService, *rabbitmq.MetadataService, *rabbitmq.ExecutorService, *rabbitmq.AuditService, *rabbitmq.PluginsMetadataService, *rabbitmq.PluginsExecutorService, *rabbitmq.PluginsAuditService) {
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
func createWebSocketHandler(cfg *config.Config, sshService ssh.SSHService, logger loggerPkg.Logger) *websocket.SSHHandler {
	if cfg.SSH.WebSocket.Enabled == nil || !*cfg.SSH.WebSocket.Enabled {
		logger.Info("WebSocket SSH is disabled in configuration")

		return nil
	}

	logger.Info("Creating WebSocket SSH handler")

	wsConfig := websocket.Config{
		ReadBufferSize:    cfg.SSH.WebSocket.ReadBufferSize,
		WriteBufferSize:   cfg.SSH.WebSocket.WriteBufferSize,
		HandshakeTimeout:  time.Duration(cfg.SSH.WebSocket.HandshakeTimeout) * time.Second,
		MaxMessageSize:    int64(cfg.SSH.WebSocket.MaxMessageSize),
		PingInterval:      time.Duration(cfg.SSH.WebSocket.PingInterval) * time.Second,
		PongTimeout:       time.Duration(cfg.SSH.WebSocket.PongTimeout) * time.Second,
		MaxSessions:       cfg.SSH.WebSocket.MaxSessions,
		SessionTimeout:    time.Duration(cfg.SSH.WebSocket.SessionTimeout) * time.Second,
		EnableCompression: cfg.SSH.WebSocket.EnableCompression,
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
func startBackgroundServices(cfg *config.Config, vault *internalVault.Vault, cfManager *internalCF.Manager, reconciler *recovery.ReconcilerAdapter, vmMonitor *vmmonitor.Monitor, ctx context.Context, logger loggerPkg.Logger) {
	// Start Vault health watcher to auto-unseal if Vault restarts and comes back sealed
	if cfg.Vault.AutoUnseal {
		interval := DefaultWSCheckInterval

		if cfg.Vault.HealthCheckInterval != "" {
			d, err := time.ParseDuration(cfg.Vault.HealthCheckInterval)
			if err == nil {
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

func startHTTPServerGoroutine(waitGroup *sync.WaitGroup, httpServer *http.Server, cancel context.CancelFunc, logger loggerPkg.Logger) {
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
}

func startHTTPSServerGoroutine(waitGroup *sync.WaitGroup, httpsServer *http.Server, cancel context.CancelFunc, logger loggerPkg.Logger) {
	if httpsServer == nil {
		return
	}

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

func startMaintenanceLoop(waitGroup *sync.WaitGroup, brokerInstance *broker.Broker, vault *internalVault.Vault, ctx context.Context, logger loggerPkg.Logger) {
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
				runMaintenanceTasks(brokerInstance, vault, ctx, logger)
			}
		}
	}()
}

func runMaintenanceTasks(brokerInstance *broker.Broker, vault *internalVault.Vault, ctx context.Context, logger loggerPkg.Logger) {
	logVaultDBState(vault, ctx, logger)
	checkServicesWithoutDeployments(brokerInstance, ctx, logger)
}

func logVaultDBState(vault *internalVault.Vault, ctx context.Context, logger loggerPkg.Logger) {
	vaultDB, err := vault.GetVaultDB(ctx)
	if err != nil {
		logger.Error("error grabbing vaultdb for debugging: %s", err)

		return
	}

	jsonData, err := json.Marshal(vaultDB.Data)
	if err != nil {
		logger.Debug("current vault db looks like: %v (json marshal error: %s)", vaultDB.Data, err)
	} else {
		logger.Debug("current vault db looks like: %s", string(jsonData))
	}
}

func checkServicesWithoutDeployments(brokerInstance *broker.Broker, ctx context.Context, logger loggerPkg.Logger) {
	_, err := brokerInstance.ServiceWithNoDeploymentCheck(ctx)
	if err != nil {
		logger.Error("service with no deployment check failed: %s", err)
	}
}

func setupGracefulShutdown(httpServer, httpsServer *http.Server, ctx context.Context, logger loggerPkg.Logger) {
	go func() {
		<-ctx.Done()
		logger.Info("Shutting down servers...")

		shutdownCtx, shutdownCancel := context.WithTimeout(ctx, DefaultShutdownTimeout)
		defer shutdownCancel()

		shutdownHTTPServer(httpServer, shutdownCtx, logger)
		shutdownHTTPSServer(httpsServer, shutdownCtx, logger)
	}()
}

func shutdownHTTPServer(httpServer *http.Server, shutdownCtx context.Context, logger loggerPkg.Logger) {
	err := httpServer.Shutdown(shutdownCtx)
	if err != nil {
		logger.Error("Error shutting down HTTP server: %s", err)
	}
}

func shutdownHTTPSServer(httpsServer *http.Server, shutdownCtx context.Context, logger loggerPkg.Logger) {
	if httpsServer != nil {
		err := httpsServer.Shutdown(shutdownCtx)
		if err != nil {
			logger.Error("Error shutting down HTTPS server: %s", err)
		}
	}
}

func runServersAndMaintenance(cfg *config.Config, apiHandler *broker.API, brokerInstance *broker.Broker, vault *internalVault.Vault, ctx context.Context, cancel context.CancelFunc, logger loggerPkg.Logger) *sync.WaitGroup {
	httpServer := startHTTPServer(cfg, apiHandler, logger)

	httpsServer, err := startHTTPSServer(cfg, apiHandler, logger)
	if err != nil {
		logger.Error("Failed to create HTTPS server: %s", err)
		os.Exit(exitCodeError)
	}

	var waitGroup sync.WaitGroup

	startHTTPServerGoroutine(&waitGroup, httpServer, cancel, logger)
	startHTTPSServerGoroutine(&waitGroup, httpsServer, cancel, logger)
	startMaintenanceLoop(&waitGroup, brokerInstance, vault, ctx, logger)
	setupGracefulShutdown(httpServer, httpsServer, ctx, logger)

	return &waitGroup
}

func setupSignalHandlingAndContext() (context.Context, context.CancelFunc, chan os.Signal) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	ctx, cancel := context.WithCancel(context.Background())

	return ctx, cancel, sigChan
}

func initializeCore(configPath string, logger loggerPkg.Logger) (*config.Config, *internalVault.Vault, *bosh.PooledDirector) {
	config := initializeConfig(configPath, logger)
	vault := initializeVault(config, logger)
	boshDirector := initializeBOSH(config, logger)
	setupBOSHResources(config, boshDirector, logger)

	return config, vault, boshDirector
}

func setupBOSHResources(config *config.Config, boshDirector bosh.Director, logger loggerPkg.Logger) {
	updateBOSHCloudConfig(config, boshDirector, logger)
	uploadBOSHReleases(config, boshDirector, logger)
	uploadBOSHStemcells(config, boshDirector, logger)
}

func getUIHandler(config *config.Config) http.Handler {
	if config.WebRoot != "" {
		return http.FileServer(http.Dir(config.WebRoot))
	}

	return &broker.NullHandler{}
}

func initializeBroker(config *config.Config, vault *internalVault.Vault, boshDirector *bosh.PooledDirector, shieldClient shield.Client, logger loggerPkg.Logger) (*broker.Broker, error) {
	brokerInstance := &broker.Broker{
		Vault:         vault,
		BOSH:          boshDirector,
		Shield:        shieldClient,
		Config:        config,
		InstanceLocks: make(map[string]*sync.Mutex),
	}

	var err error

	if len(os.Args) > DefaultMinArgsRequired {
		logger.Info("reading services from CLI arguments: %s", strings.Join(os.Args[3:], ", "))
		err = brokerInstance.ReadServices(os.Args[3:]...)
	} else {
		logger.Info("no CLI arguments provided, using configuration-based service discovery")

		err = brokerInstance.ReadServices()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to read SERVICE directories: %w", err)
	}

	return brokerInstance, nil
}

func initializeCFManager(config *config.Config, logger loggerPkg.Logger) *internalCF.Manager {
	if len(config.Broker.CF.APIs) == 0 {
		logger.Info("CF reconciliation disabled: no CF API endpoints configured")

		return nil
	}

	logger.Info("initializing CF connection manager with %d endpoint(s)", len(config.Broker.CF.APIs))

	// Convert main CFAPIConfig to internal CFAPIConfig
	internalAPIs := make(map[string]internalCF.ExternalCFAPIConfig)
	for name, apiConfig := range config.Broker.CF.APIs {
		internalAPIs[name] = internalCF.ExternalCFAPIConfig{
			Name:     apiConfig.Name,
			Endpoint: apiConfig.Endpoint,
			Username: apiConfig.Username,
			Password: apiConfig.Password,
		}
	}

	cfManager := internalCF.NewManagerFromExternal(internalAPIs, logger.Named("cf-manager"))
	logger.Info("CF connection manager initialized successfully")

	return cfManager
}

func createAPIHandler(config *config.Config, brokerInstance *broker.Broker, vault *internalVault.Vault, boshDirector *bosh.PooledDirector, cfManager *internalCF.Manager,
	vmMonitor *vmmonitor.Monitor, sshService *ssh.ServiceImpl, rabbitmqSSHService *rabbitmq.SSHService,
	rabbitmqMetadataService *rabbitmq.MetadataService, rabbitmqExecutorService *rabbitmq.ExecutorService,
	rabbitmqAuditService *rabbitmq.AuditService, rabbitmqPluginsMetadataService *rabbitmq.PluginsMetadataService,
	rabbitmqPluginsExecutorService *rabbitmq.PluginsExecutorService, rabbitmqPluginsAuditService *rabbitmq.PluginsAuditService,
	webSocketHandler *websocket.SSHHandler, uiHandler http.Handler, logger loggerPkg.Logger) *broker.API {
	internalAPI := api.NewInternalAPI(api.Dependencies{
		Config:                         config,
		Logger:                         logger,
		Vault:                          vault,
		Broker:                         brokerInstance,
		Director:                       boshDirector,
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

	return &broker.API{
		Username: config.Broker.Username,
		Password: config.Broker.Password,
		WebRoot:  uiHandler,
		Logger:   logger.Named("api"),
		Internal: internalAPI,
		Primary: brokerapi.New(
			brokerInstance,
			lager.NewLogger("blacksmith-broker"),
			brokerapi.BrokerCredentials{
				Username: config.Broker.Username,
				Password: config.Broker.Password,
			},
		),
	}
}

// runService runs the main service logic.
func runService(configPath string, buildInfo BuildInfo, logger loggerPkg.Logger) error {
	config, vault, boshDirector := initializeCore(configPath, logger)

	shieldClient := initializeShieldClient(config, logger)

	brokerInstance, err := initializeBroker(config, vault, boshDirector, shieldClient, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize broker: %w", err)
	}

	uiHandler := getUIHandler(config)

	logger.Info("blacksmith service broker v%s starting up...", buildInfo.Version)

	// Setup signal handling and context
	ctx, cancel, sigChan := setupSignalHandlingAndContext()
	defer cancel()

	// Initialize CF connection manager
	cfManager := initializeCFManager(config, logger)

	// Initialize the deployment reconciler
	reconciler := recovery.NewReconcilerAdapter(config, brokerInstance, vault, boshDirector, cfManager)

	// Initialize the VM monitor
	vmMonitor := vmmonitor.New(vault, boshDirector, config)

	// Start background services
	startBackgroundServices(config, vault, cfManager, reconciler, vmMonitor, ctx, logger)

	// Initialize SSH and RabbitMQ services
	sshService, rabbitmqSSHService, rabbitmqMetadataService, rabbitmqExecutorService, rabbitmqAuditService, rabbitmqPluginsMetadataService, rabbitmqPluginsExecutorService, rabbitmqPluginsAuditService, webSocketHandler := initializeServices(config, brokerInstance, vault, logger)

	// Ensure SSH service is closed on shutdown
	defer func() {
		if sshService != nil {
			err := sshService.Close()
			if err != nil {
				logger.Error("Error closing SSH service: %s", err)
			}
		}
	}()

	// Create the main API handler with refactored InternalAPI
	apiHandler := createAPIHandler(config, brokerInstance, vault, boshDirector, cfManager, vmMonitor,
		sshService, rabbitmqSSHService, rabbitmqMetadataService, rabbitmqExecutorService,
		rabbitmqAuditService, rabbitmqPluginsMetadataService, rabbitmqPluginsExecutorService,
		rabbitmqPluginsAuditService, webSocketHandler, uiHandler, logger)

	// Start servers and maintenance loops
	serverWaitGroup := runServersAndMaintenance(config, apiHandler, brokerInstance, vault, ctx, cancel, logger)

	// Wait for shutdown signal
	select {
	case sig := <-sigChan:
		logger.Info("Received signal %s, shutting down gracefully...", sig)
	case <-ctx.Done():
		logger.Info("Context cancelled, shutting down...")
	}

	cancel()               // Cancel context to stop other goroutines
	serverWaitGroup.Wait() // Wait for all goroutines to finish

	return nil
}

func main() {
	// Initialize build info
	buildInfo := GetBuildInfo()

	// Parse command line flags
	configPath := parseFlags(buildInfo)

	// Initialize centralized logger from environment
	err := loggerPkg.InitFromEnv()
	if err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}

	// Get the global logger instance
	logger := loggerPkg.Get().Named("main")

	// Log build version information at startup
	logger.Info("blacksmith starting - version: %s, build: %s, commit: %s, go: %s",
		buildInfo.Version, buildInfo.BuildTime, buildInfo.GitCommit, runtime.Version())

	// Initialize and run the service
	err = runService(configPath, *buildInfo, logger)
	if err != nil {
		logger.Error("Service failed: %v", err)
		os.Exit(exitCodeError)
	}

	logger.Info("Blacksmith service broker shut down complete")
}

// Use SSH.WebSocket.PingInterval as the authoritative setting for WS ping tuning.
