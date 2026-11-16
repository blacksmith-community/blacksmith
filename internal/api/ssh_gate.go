package api

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"blacksmith/internal/apierr"
	"blacksmith/internal/interfaces"
	"blacksmith/internal/upgrade"
	pkgmiddleware "blacksmith/pkg/http/middleware"
	"blacksmith/pkg/http/response"
)

// errSSHDisabled is what a runner reports when ssh.enabled is false.
var errSSHDisabled = upgrade.ErrSSHDisabled

// sshBackedRoutes are the routes served by handlers that open an SSH session
// to a VM, or that belong to the SSH terminal. ssh.enabled false refuses each
// of them with a 403. The upgrade routes that run commands, and the patch
// mode of certificate tasks, are refused by the upgrade handler and manager.
//
//nolint:gochecknoglobals // a fixed table of compiled patterns
var sshBackedRoutes = []*regexp.Regexp{
	// The websocket terminal and its status.
	regexp.MustCompile(`^/b/blacksmith/ssh/stream$`),
	regexp.MustCompile(`^/b/ssh/status$`),
	regexp.MustCompile(`^/b/[^/]+/ssh/(stream|execute)$`),
	// RabbitMQ commands, which run over SSH.
	regexp.MustCompile(`^/b/[^/]+/rabbitmq/ssh/.+$`),
	regexp.MustCompile(`^/b/[^/]+/rabbitmq/(rabbitmqctl|plugins)/.+$`),
	// The trusted certificate files, which are read and written over SSH.
	regexp.MustCompile(`^/b/[^/]+/certificates/trusted(/file)?$`),
}

// brokerCertificatesPrefix starts the broker's own certificate routes,
// which read files on the broker VM and open no SSH session. The pattern
// for an instance's trusted certificates would otherwise match them, since
// internal fills the instance segment.
const brokerCertificatesPrefix = "/b/internal/certificates/"

// isSSHBackedRoute reports whether the path is served by an SSH-backed
// handler.
func isSSHBackedRoute(path string) bool {
	if strings.HasPrefix(path, brokerCertificatesPrefix) {
		return false
	}

	for _, route := range sshBackedRoutes {
		if route.MatchString(path) {
			return true
		}
	}

	return false
}

// sshGate refuses a request for an SSH-backed route with a 403 while
// ssh.enabled is false. It sits inside the router's middleware chain.
func sshGate(sshEnabled func() bool) pkgmiddleware.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			if sshEnabled != nil && !sshEnabled() && isSSHBackedRoute(req.URL.Path) {
				refuseSSHDisabled(writer)

				return
			}

			next.ServeHTTP(writer, req)
		})
	}
}

// refuseSSHDisabled answers 403 with the SSH-disabled code and message.
func refuseSSHDisabled(writer http.ResponseWriter) {
	response.WriteErrorCode(writer, http.StatusForbidden, apierr.CodeSSHDisabled, upgrade.SSHDisabledMessage)
}

// gateRunner makes a runner that opens SSH sessions refuse when ssh.enabled
// is false, so a task that was created before the switch was turned off
// opens no session either.
func gateRunner(cfg interfaces.Config, runner func(ctx context.Context, deployment, group string, indices []int) (upgrade.PatchSession, error)) func(ctx context.Context, deployment, group string, indices []int) (upgrade.PatchSession, error) {
	return func(ctx context.Context, deployment, group string, indices []int) (upgrade.PatchSession, error) {
		if !cfg.IsSSHEnabled() {
			return nil, errSSHDisabled
		}

		return runner(ctx, deployment, group, indices)
	}
}
