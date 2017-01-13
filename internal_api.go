package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
)

type InternalApi struct {
	Vault    *Vault
	Broker   *Broker
	Username string
	Password string
}

func (api *InternalApi) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	var pattern *regexp.Regexp
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

	pattern = regexp.MustCompile("^/b/([^/]+)/manifest$")
	if m := pattern.FindStringSubmatch(req.URL.Path); m != nil {
		d, exists, err := api.Vault.Get(fmt.Sprintf("%s/manifest", m[1]))
		if err == nil && exists {
			if s, ok := d["manifest"]; ok {
				w.Header().Set("Content-type", "text/plain")
				fmt.Fprintf(w, "%v\n", s)
				return
			}
		}
		w.WriteHeader(404)
		return
	}

	w.WriteHeader(404)
	fmt.Fprintf(w, "endpoint not found...\n")
}
