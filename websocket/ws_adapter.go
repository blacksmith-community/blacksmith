package websocket

import (
	"net/http"
	wsn "nhooyr.io/websocket"
)

// acceptWS wraps nhooyr Accept and returns a WSConn abstraction
func acceptWS(w http.ResponseWriter, r *http.Request, enableCompression bool) (WSConn, error) {
	opts := &wsn.AcceptOptions{}
	if enableCompression {
		opts.CompressionMode = wsn.CompressionNoContextTakeover
	}
	c, err := wsn.Accept(w, r, opts)
	if err != nil {
		return nil, err
	}
	return &nhooyrWSConn{conn: c}, nil
}
