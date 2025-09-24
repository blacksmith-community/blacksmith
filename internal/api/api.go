package api

import (
	"net/http"

	"blacksmith/internal/handlers/blacksmith"
	"blacksmith/internal/handlers/bosh"
	"blacksmith/internal/handlers/certificates"
	"blacksmith/internal/handlers/cf"
	"blacksmith/internal/handlers/configuration"
	"blacksmith/internal/handlers/deployments"
	"blacksmith/internal/handlers/instances"
	serviceshandler "blacksmith/internal/handlers/services"
	"blacksmith/internal/handlers/services/rabbitmq/websocket"
	"blacksmith/internal/handlers/services/redis"
	sshwebsocket "blacksmith/internal/handlers/ssh/websocket"
	"blacksmith/internal/handlers/tasks"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/middleware"
	"blacksmith/internal/routing"
	pkgmiddleware "blacksmith/pkg/http/middleware"
	"blacksmith/pkg/services"
)

// InternalAPI represents the refactored internal API with proper separation of concerns.
type InternalAPI struct {
	router                   *routing.Router
	certificateHandler       *certificates.Handler
	cfHandler                *cf.Handler
	instanceHandler          *instances.Handler
	redisHandler             *redis.Handler
	rabbitMQWebSocketHandler *websocket.Handler
	sshWebSocketHandler      *sshwebsocket.Handler
	boshHandler              *bosh.Handler
	blacksmithHandler        *blacksmith.Handler
	tasksHandler             *tasks.Handler
	deploymentsHandler       *deployments.Handler
	configurationHandler     *configuration.Handler
	servicesHandler          *serviceshandler.Handler
}

// Dependencies contains all the dependencies needed by the internal API.
type Dependencies struct {
	Config                         interfaces.Config
	Logger                         interfaces.Logger
	Vault                          interfaces.Vault
	Broker                         interfaces.Broker
	Director                       interfaces.Director
	ServicesManager                *services.Manager
	CFManager                      interfaces.CFManager
	VMMonitor                      interfaces.VMMonitor
	SSHService                     interfaces.SSHService
	RabbitMQSSHService             interfaces.RabbitMQSSHService
	RabbitMQMetadataService        interfaces.RabbitMQMetadataService
	RabbitMQExecutorService        interfaces.RabbitMQExecutorService
	RabbitMQAuditService           interfaces.RabbitMQAuditService
	RabbitMQPluginsMetadataService interfaces.RabbitMQPluginsMetadataService
	RabbitMQPluginsExecutorService interfaces.RabbitMQPluginsExecutorService
	RabbitMQPluginsAuditService    interfaces.RabbitMQPluginsAuditService
	WebSocketHandler               interfaces.WebSocketHandler
	SecurityMiddleware             *services.SecurityMiddleware
}

// NewInternalAPI creates a new internal API with the refactored structure.
type apiHandlers struct {
	certificate       *certificates.Handler
	cf                *cf.Handler
	instance          *instances.Handler
	redis             *redis.Handler
	rabbitMQWebSocket *websocket.Handler
	sshWebSocket      *sshwebsocket.Handler
	bosh              *bosh.Handler
	blacksmith        *blacksmith.Handler
	tasks             *tasks.Handler
	deployments       *deployments.Handler
	configuration     *configuration.Handler
	services          *serviceshandler.Handler
}

func createHandlers(deps Dependencies) apiHandlers {
	return apiHandlers{
		certificate:       certificates.NewHandler(deps.Config, deps.Logger, deps.Broker),
		cf:                cf.NewHandler(deps.Logger, deps.Config, deps.CFManager, deps.Vault),
		instance:          instances.NewHandler(deps.Logger),
		redis:             redis.NewHandler(deps.Logger, deps.Vault, deps.ServicesManager),
		rabbitMQWebSocket: createRabbitMQWebSocketHandler(deps),
		sshWebSocket:      createSSHWebSocketHandler(deps),
		bosh:              createBOSHHandler(deps),
		blacksmith:        createBlacksmithHandler(deps),
		tasks:             createTasksHandler(deps),
		deployments:       createDeploymentsHandler(deps),
		configuration:     createConfigurationHandler(deps),
		services:          createServicesHandler(deps),
	}
}

func createRabbitMQWebSocketHandler(deps Dependencies) *websocket.Handler {
	return websocket.NewHandler(websocket.Dependencies{
		Logger:                         deps.Logger,
		RabbitMQExecutorService:        deps.RabbitMQExecutorService,
		RabbitMQPluginsExecutorService: deps.RabbitMQPluginsExecutorService,
		RabbitMQAuditService:           deps.RabbitMQAuditService,
		RabbitMQPluginsAuditService:    deps.RabbitMQPluginsAuditService,
	})
}

func createSSHWebSocketHandler(deps Dependencies) *sshwebsocket.Handler {
	return sshwebsocket.NewHandler(sshwebsocket.Dependencies{
		Logger:           deps.Logger,
		Config:           deps.Config,
		WebSocketHandler: deps.WebSocketHandler,
	})
}

func createBOSHHandler(deps Dependencies) *bosh.Handler {
	return bosh.NewHandler(bosh.Dependencies{
		Logger:   deps.Logger,
		Config:   deps.Config,
		Vault:    deps.Vault,
		Director: deps.Director,
		Broker:   deps.Broker,
	})
}

func createBlacksmithHandler(deps Dependencies) *blacksmith.Handler {
	return blacksmith.NewHandler(blacksmith.Dependencies{
		Logger:   deps.Logger,
		Config:   deps.Config,
		Vault:    deps.Vault,
		Director: deps.Director,
	})
}

func createTasksHandler(deps Dependencies) *tasks.Handler {
	return tasks.NewHandler(tasks.Dependencies{
		Logger:   deps.Logger,
		Config:   deps.Config,
		Director: deps.Director,
	})
}

func createDeploymentsHandler(deps Dependencies) *deployments.Handler {
	return deployments.NewHandler(deployments.Dependencies{
		Logger:   deps.Logger,
		Config:   deps.Config,
		Vault:    deps.Vault,
		Director: deps.Director,
	})
}

func createConfigurationHandler(deps Dependencies) *configuration.Handler {
	return configuration.NewHandler(configuration.Dependencies{
		Logger: deps.Logger,
		Config: deps.Config,
		Vault:  deps.Vault,
	})
}

func createServicesHandler(deps Dependencies) *serviceshandler.Handler {
	return serviceshandler.NewHandler(serviceshandler.Dependencies{
		Logger:   deps.Logger,
		Config:   deps.Config,
		Vault:    deps.Vault,
		Director: deps.Director,
	})
}

func createRouterWithMiddleware(deps Dependencies) *routing.Router {
	middlewareChain := pkgmiddleware.New(
		middleware.LoggingMiddleware(deps.Logger),
		middleware.SecurityMiddleware(deps.SecurityMiddleware),
	)

	return routing.NewRouter(middlewareChain)
}

func handleServiceRouting(writer http.ResponseWriter, req *http.Request, handlers apiHandlers) {
	if handlers.rabbitMQWebSocket.CanHandle(req.URL.Path) {
		handlers.rabbitMQWebSocket.ServeHTTP(writer, req)

		return
	}

	if handlers.redis.CanHandle(req.URL.Path) {
		handlers.redis.ServeHTTP(writer, req)

		return
	}

	if handlers.sshWebSocket.CanHandle(req.URL.Path) {
		handlers.sshWebSocket.ServeHTTP(writer, req)

		return
	}

	if handlers.services.CanHandle(req.URL.Path) {
		handlers.services.ServeHTTP(writer, req)

		return
	}

	if handlers.configuration.CanHandle(req.URL.Path) {
		handlers.configuration.ServeHTTP(writer, req)

		return
	}

	writer.WriteHeader(http.StatusNotFound)
	_, _ = writer.Write([]byte("endpoint not found"))
}

func registerRoutes(router *routing.Router, handlers apiHandlers, deps Dependencies) {
	// Certificate endpoints
	router.RegisterHandler("/b/internal/certificates", http.HandlerFunc(handlers.certificate.HandleCertificatesRequest))
	router.RegisterHandler("/b/certificates/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte("certificate listing not yet implemented"))
	}))

	// CF endpoints
	router.RegisterHandler("/b/cf/", handlers.cf)

	// Instance endpoints
	router.RegisterHandler("/b/instance", http.HandlerFunc(handlers.instance.GetInstanceDetails))
	router.RegisterHandler("/b/config/ssh/ui-terminal-status", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		handlers.instance.GetSSHUITerminalStatus(w, req, deps.Config.IsSSHUITerminalEnabled())
	}))

	// BOSH endpoints
	router.RegisterHandler("/b/bosh/pool-stats", http.HandlerFunc(handlers.bosh.GetPoolStats))
	router.RegisterHandler("/b/status", http.HandlerFunc(handlers.bosh.GetStatus))

	// Blacksmith management endpoints
	router.RegisterHandler("/b/blacksmith/logs", http.HandlerFunc(handlers.blacksmith.GetLogs))
	router.RegisterHandler("/b/blacksmith/vms", http.HandlerFunc(handlers.blacksmith.GetVMs))
	router.RegisterHandler("/b/blacksmith/events", http.HandlerFunc(handlers.blacksmith.GetEvents))
	router.RegisterHandler("/b/blacksmith/manifest", http.HandlerFunc(handlers.blacksmith.GetManifest))
	router.RegisterHandler("/b/blacksmith/credentials", http.HandlerFunc(handlers.blacksmith.GetCredentials))
	router.RegisterHandler("/b/blacksmith/config", http.HandlerFunc(handlers.blacksmith.GetConfig))
	router.RegisterHandler("/b/cleanup", http.HandlerFunc(handlers.blacksmith.Cleanup))

	// Task endpoints
	router.RegisterHandler("/b/tasks", handlers.tasks)

	// Deployment endpoints
	router.RegisterHandler("/b/deployments/", handlers.deployments)

	// Configuration endpoints
	router.RegisterHandler("/b/configs", http.HandlerFunc(handlers.configuration.GetConfigs))
	router.RegisterHandler("/b/service-filter-options", http.HandlerFunc(handlers.configuration.GetServiceFilterOptions))

	// Credentials endpoint - returns all service instance credentials
	router.RegisterHandler("/creds", http.HandlerFunc(handlers.services.GetAllCredentials))

	// SSH WebSocket handlers
	router.RegisterHandler("/b/blacksmith/ssh/stream", handlers.sshWebSocket)
	router.RegisterHandler("/b/ssh/status", handlers.sshWebSocket)

	// Service-specific handlers - register last for pattern matching
	router.RegisterHandler("/b/", http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		handleServiceRouting(writer, req, handlers)
	}))
}

func NewInternalAPI(deps Dependencies) *InternalAPI {
	// Create all handlers
	handlers := createHandlers(deps)

	// Create middleware and router
	router := createRouterWithMiddleware(deps)

	// Register all routes
	registerRoutes(router, handlers, deps)

	// Create and return the API instance
	return &InternalAPI{
		router:                   router,
		certificateHandler:       handlers.certificate,
		cfHandler:                handlers.cf,
		instanceHandler:          handlers.instance,
		redisHandler:             handlers.redis,
		rabbitMQWebSocketHandler: handlers.rabbitMQWebSocket,
		sshWebSocketHandler:      handlers.sshWebSocket,
		boshHandler:              handlers.bosh,
		blacksmithHandler:        handlers.blacksmith,
		tasksHandler:             handlers.tasks,
		deploymentsHandler:       handlers.deployments,
		configurationHandler:     handlers.configuration,
		servicesHandler:          handlers.services,
	}
}

// ServeHTTP implements the http.Handler interface.
func (api *InternalAPI) ServeHTTP(writer http.ResponseWriter, req *http.Request) {
	// Use the router to find and execute the appropriate handler
	if handler := api.router.FindHandler(req.URL.Path); handler != nil {
		handler.ServeHTTP(writer, req)

		return
	}

	// No handler found, return 404
	writer.WriteHeader(http.StatusNotFound)
	_, _ = writer.Write([]byte("endpoint not found"))
}
