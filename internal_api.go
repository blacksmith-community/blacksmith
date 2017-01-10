package main

import (
	"encoding/json"
	"fmt"
	"net/http"
)

type InternalApi struct {
	Vault    *Vault
	Broker   *Broker
	Username string
	Password string
}

func (api *InternalApi) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	username, password, ok := req.BasicAuth()
	if !ok {
		w.WriteHeader(401)
		fmt.Fprintf(w, "Authorization Required\n")
		return
	}
	if username != api.Username || password != api.Password {
		w.WriteHeader(403)
		fmt.Fprintf(w, "Forbidden\n")
		return
	}

	if req.URL.Path == "/b/status" {
		idx, err := api.Vault.GetIndex("db")
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s\n", idx.JSON())
		return
	}

	if req.URL.Path == "/b/plans" {
		p := deinterface(api.Broker.Plans)
		b, err := json.Marshal(p)
		if err != nil {
			w.WriteHeader(500)
			fmt.Fprintf(w, "error: %s\n", err)
			return
		}

		w.Header().Set("Content-type", "application/json")
		fmt.Fprintf(w, "%s\n", string(b))
		return
	}

	fmt.Fprintf(w, "...\n")
}
