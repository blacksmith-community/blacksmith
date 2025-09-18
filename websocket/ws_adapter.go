package websocket

import (
	"fmt"
	"net/http"
	wsn "nhooyr.io/websocket"
)

// acceptWS wraps nhooyr Accept and returns a WSConn abstraction.
func acceptWS(writer http.ResponseWriter, request *http.Request, enableCompression bool) (*nhooyrWSConn, error) {
	opts := &wsn.AcceptOptions{}
	if enableCompression {
		opts.CompressionMode = wsn.CompressionNoContextTakeover
	}

	c, err := wsn.Accept(writer, request, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to accept websocket connection: %w", err)
	}

	return &nhooyrWSConn{conn: c}, nil
}
