// Package sshconn holds the SSH connection code that the director
// adapter and the SSH terminal share, so both reach a VM through one
// dial whose handshake ends with its context.
package sshconn

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// sshPort is the port that sshd listens on, on every BOSH VM.
const sshPort = "22"

// Client is the part of an SSH client that running a command needs. A
// client reached through a gateway also owns the gateway connection.
type Client interface {
	NewSession() (*ssh.Session, error)
	Close() error
}

// Address returns the SSH address of host, bracketing an IPv6 literal.
func Address(host string) string {
	return net.JoinHostPort(host, sshPort)
}

// DialDirect connects to addr and runs the SSH handshake. The TCP
// connect and the handshake both end with ctx, and with config.Timeout
// when ctx has no deadline, so a server that accepts and never speaks
// cannot hold the caller.
func DialDirect(ctx context.Context, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	conn, err := (&net.Dialer{Timeout: config.Timeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}

	return handshakeSSH(ctx, conn, addr, config)
}

// DialGateway connects to targetAddr through the SSH gateway at
// gatewayAddr. Every step ends with ctx. The returned client owns the
// gateway client, so closing it closes both.
//
//nolint:ireturn // The caller needs only Client, and the concrete type also owns the gateway.
func DialGateway(ctx context.Context, gatewayAddr, targetAddr string, gatewayConfig, config *ssh.ClientConfig) (Client, error) {
	gatewayConn, err := (&net.Dialer{Timeout: gatewayConfig.Timeout}).DialContext(ctx, "tcp", gatewayAddr)
	if err != nil {
		return nil, fmt.Errorf("connect to gateway %s: %w", gatewayAddr, err)
	}

	gatewayClient, err := handshakeSSH(ctx, gatewayConn, gatewayAddr, gatewayConfig)
	if err != nil {
		return nil, fmt.Errorf("gateway: %w", err)
	}

	conn, err := gatewayClient.DialContext(ctx, "tcp", targetAddr)
	if err != nil {
		_ = gatewayClient.Close()

		return nil, fmt.Errorf("connect to %s through gateway %s: %w", targetAddr, gatewayAddr, err)
	}

	targetClient, err := handshakeSSH(ctx, conn, targetAddr, config)
	if err != nil {
		_ = gatewayClient.Close()

		return nil, fmt.Errorf("through gateway %s: %w", gatewayAddr, err)
	}

	return &gatewayedClient{Client: targetClient, gateway: gatewayClient}, nil
}

// gatewayedClient is a target client that owns the gateway client its
// connection is tunnelled through, so closing it closes both.
type gatewayedClient struct {
	*ssh.Client

	gateway *ssh.Client
}

// Close closes the target client and the gateway client. It always closes
// both and returns the first error.
func (c *gatewayedClient) Close() error {
	err := c.Client.Close()
	if gatewayErr := c.gateway.Close(); err == nil {
		err = gatewayErr
	}

	return err
}

// closeOnceConn closes the connection underneath it at most once, however
// many callers close it. The SSH library closes a connection when its
// handshake fails, and the SSH client closes it again later, so every close
// goes through here. The first closer gets the real result and later closers
// get net.ErrClosed.
type closeOnceConn struct {
	net.Conn

	once sync.Once
	// closedByContext records that the context watcher made the real close.
	closedByContext atomic.Bool
}

func (c *closeOnceConn) Close() error {
	err := net.ErrClosed

	c.once.Do(func() { err = c.Conn.Close() })

	return err //nolint:wrapcheck // A net.Conn wrapper returns its connection's close error as it is.
}

// closeForContext is the context watcher's close. It marks the connection as
// closed by the context only when it is the close that takes effect.
func (c *closeOnceConn) closeForContext() {
	c.once.Do(func() {
		c.closedByContext.Store(true)

		_ = c.Conn.Close()
	})
}

// handshakeSSH runs the SSH handshake on conn, bounded by ctx. The
// ClientConfig Timeout only bounds the TCP connect, so a server that accepts
// and never speaks would otherwise block the handshake forever. The
// handshake therefore runs under the context's deadline, or under the
// config's Timeout when the context has none, and ends early when the
// context is cancelled. conn is closed exactly once when the handshake
// fails, and is left open for the returned client when it succeeds. Every
// error names addr.
func handshakeSSH(ctx context.Context, rawConn net.Conn, addr string, config *ssh.ClientConfig) (*ssh.Client, error) {
	conn := &closeOnceConn{Conn: rawConn}

	if deadline, ok := ctx.Deadline(); ok {
		// Tunnelled channels do not support deadlines; the AfterFunc below
		// still ends their handshake.
		_ = conn.SetDeadline(deadline)
	} else if config.Timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(config.Timeout))
	}

	stop := context.AfterFunc(ctx, conn.closeForContext)

	nconn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	stopped := stop()

	if err != nil {
		// The library closed the connection already; this keeps the
		// guarantee independent of that.
		_ = conn.Close()

		// Report the context only when its watcher closed the connection
		// under the handshake. Any other failure is the handshake's own.
		if conn.closedByContext.Load() {
			return nil, fmt.Errorf("SSH handshake with %s ended with its context: %w", addr, context.Cause(ctx))
		}

		return nil, fmt.Errorf("SSH handshake with %s: %w", addr, err)
	}

	if !stopped {
		// The context ended after the handshake completed and its watcher
		// is closing the connection, so the client would be dead.
		_ = conn.Close()

		return nil, fmt.Errorf("SSH handshake with %s ended with its context: %w", addr, context.Cause(ctx))
	}

	// The handshake deadline must not cut off the session that follows.
	_ = conn.SetDeadline(time.Time{})

	return ssh.NewClient(nconn, chans, reqs), nil
}
