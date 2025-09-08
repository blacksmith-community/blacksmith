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
func NewInternalAPI(deps Dependencies) *InternalAPI {
	// Create handlers
	certificateHandler := certificates.NewHandler(deps.Config, deps.Logger, deps.Broker)
	cfHandler := cf.NewHandler(deps.Logger, deps.CFManager, deps.Vault)
	instanceHandler := instances.NewHandler(deps.Logger)
	redisHandler := redis.NewHandler(deps.Logger, deps.Vault, deps.ServicesManager)

	// Create RabbitMQ WebSocket handler
	rabbitMQWebSocketHandler := websocket.NewHandler(websocket.Dependencies{
		Logger:                         deps.Logger,
		RabbitMQExecutorService:        deps.RabbitMQExecutorService,
		RabbitMQPluginsExecutorService: deps.RabbitMQPluginsExecutorService,
		RabbitMQAuditService:           deps.RabbitMQAuditService,
		RabbitMQPluginsAuditService:    deps.RabbitMQPluginsAuditService,
	})

	// Create SSH WebSocket handler
	sshWebSocketHandler := sshwebsocket.NewHandler(sshwebsocket.Dependencies{
		Logger:           deps.Logger,
		Config:           deps.Config,
		WebSocketHandler: deps.WebSocketHandler,
	})

	// Create management handlers
	boshHandler := bosh.NewHandler(bosh.Dependencies{
		Logger: deps.Logger,
		Config: deps.Config,
		Vault:  deps.Vault,
	})

	blacksmithHandler := blacksmith.NewHandler(blacksmith.Dependencies{
		Logger: deps.Logger,
		Config: deps.Config,
		Vault:  deps.Vault,
	})

	tasksHandler := tasks.NewHandler(tasks.Dependencies{
		Logger: deps.Logger,
		Config: deps.Config,
	})

	deploymentsHandler := deployments.NewHandler(deployments.Dependencies{
		Logger: deps.Logger,
		Config: deps.Config,
		Vault:  deps.Vault,
	})

	configurationHandler := configuration.NewHandler(configuration.Dependencies{
		Logger: deps.Logger,
		Config: deps.Config,
		Vault:  deps.Vault,
	})

	servicesHandler := serviceshandler.NewHandler(serviceshandler.Dependencies{
		Logger: deps.Logger,
		Config: deps.Config,
		Vault:  deps.Vault,
	})

	// Create middleware chain
	middlewareChain := pkgmiddleware.New(
		middleware.LoggingMiddleware(deps.Logger),
		middleware.SecurityMiddleware(deps.SecurityMiddleware),
	)

	// Create router and register handlers
	router := routing.NewRouter(middlewareChain)

	// Register route handlers
	// Certificate endpoints
	router.RegisterHandler("/b/certificates/", certificateHandler)

	// CF endpoints
	router.RegisterHandler("/b/cf/", cfHandler)

	// Instance endpoints
	router.RegisterHandler("/b/instance", http.HandlerFunc(instanceHandler.GetInstanceDetails))
	router.RegisterHandler("/b/config/ssh/ui-terminal-status", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		instanceHandler.GetSSHUITerminalStatus(w, req, deps.Config.IsSSHUITerminalEnabled())
	}))

	// BOSH endpoints
	router.RegisterHandler("/b/bosh/pool-stats", http.HandlerFunc(boshHandler.GetPoolStats))
	router.RegisterHandler("/b/status", http.HandlerFunc(boshHandler.GetStatus))

	// Blacksmith management endpoints
	router.RegisterHandler("/b/blacksmith/logs", http.HandlerFunc(blacksmithHandler.GetLogs))
	router.RegisterHandler("/b/blacksmith/credentials", http.HandlerFunc(blacksmithHandler.GetCredentials))
	router.RegisterHandler("/b/blacksmith/config", http.HandlerFunc(blacksmithHandler.GetConfig))
	router.RegisterHandler("/b/cleanup", http.HandlerFunc(blacksmithHandler.Cleanup))

	// Task endpoints
	router.RegisterHandler("/b/tasks", tasksHandler)

	// Deployment endpoints
	router.RegisterHandler("/b/deployments/", deploymentsHandler)

	// Configuration endpoints
	router.RegisterHandler("/b/configs", http.HandlerFunc(configurationHandler.GetConfigs))
	router.RegisterHandler("/b/service-filter-options", http.HandlerFunc(configurationHandler.GetServiceFilterOptions))

	// SSH WebSocket handlers
	router.RegisterHandler("/b/blacksmith/ssh/stream", sshWebSocketHandler)
	router.RegisterHandler("/b/ssh/status", sshWebSocketHandler)

	// Service-specific handlers - these use pattern matching so register them last
	// This is a catch-all for dynamic service patterns
	router.RegisterHandler("/b/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		// Check for RabbitMQ WebSocket patterns
		if rabbitMQWebSocketHandler.CanHandle(req.URL.Path) {
			rabbitMQWebSocketHandler.ServeHTTP(w, req)

			return
		}
		// Check for Redis patterns
		if redisHandler.CanHandle(req.URL.Path) {
			redisHandler.ServeHTTP(w, req)

			return
		}
		// Check for SSH patterns (service instance SSH)
		if sshWebSocketHandler.CanHandle(req.URL.Path) {
			sshWebSocketHandler.ServeHTTP(w, req)

			return
		}
		// Check for generic service patterns
		if servicesHandler.CanHandle(req.URL.Path) {
			servicesHandler.ServeHTTP(w, req)

			return
		}
		// Check for configuration patterns
		if configurationHandler.CanHandle(req.URL.Path) {
			configurationHandler.ServeHTTP(w, req)

			return
		}
		// No handler found
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("endpoint not found"))
	}))

	return &InternalAPI{
		router:                   router,
		certificateHandler:       certificateHandler,
		cfHandler:                cfHandler,
		instanceHandler:          instanceHandler,
		redisHandler:             redisHandler,
		rabbitMQWebSocketHandler: rabbitMQWebSocketHandler,
		sshWebSocketHandler:      sshWebSocketHandler,
		boshHandler:              boshHandler,
		blacksmithHandler:        blacksmithHandler,
		tasksHandler:             tasksHandler,
		deploymentsHandler:       deploymentsHandler,
		configurationHandler:     configurationHandler,
		servicesHandler:          servicesHandler,
	}
}

// ServeHTTP implements the http.Handler interface.
func (api *InternalAPI) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Use the router to find and execute the appropriate handler
	if handler := api.router.FindHandler(req.URL.Path); handler != nil {
		handler.ServeHTTP(w, req)

		return
	}

	// No handler found, return 404
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("endpoint not found"))
}
