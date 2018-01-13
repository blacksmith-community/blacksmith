package main

import (
	"fmt"
	"net/http"
	"strings"
)

type API struct {
	Username string
	Password string
	Internal http.Handler
	Primary  http.Handler
	WebRoot  http.Handler
}

func (api API) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	username, password, ok := req.BasicAuth()
	if !ok {
		w.Header().Set("WWW-Authenticate", "basic realm=Blacksmith")
		w.WriteHeader(401)
		fmt.Fprintf(w, "Authorization Required\n")
		return
	}
	if username != api.Username || password != api.Password {
		w.WriteHeader(403)
		fmt.Fprintf(w, "Forbidden\n")
		return
	}

	if strings.HasPrefix(req.URL.Path, "/b/") {
		api.Internal.ServeHTTP(w, req)
		return
	}

	if strings.HasPrefix(req.URL.Path, "/v2/") {
		api.Primary.ServeHTTP(w, req)
		return
	}

	api.WebRoot.ServeHTTP(w, req)
}

type NullHandler struct{}

func (n NullHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	w.WriteHeader(404)
	fmt.Fprintf(w, "404 not found\n")
}
