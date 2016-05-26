package main

import (
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/pivotal-cf/brokerapi"
	"github.com/pivotal-golang/lager"
)

var (
	AuthUsername string
	AuthPassword string
)

func main() {
	AuthUsername = os.Getenv("AUTH_USERNAME")
	if AuthUsername == "" {
		AuthUsername = "blacksmith"
	}

	AuthPassword = os.Getenv("AUTH_PASSWORD")
	if AuthPassword == "" {
		AuthPassword = "blacksmith"
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	bind := fmt.Sprintf(":%s", port)

	broker := EnvBroker()
	if broker == nil {
		os.Exit(1)
	}

	log.Printf("Blacksmith Service Broker listening on %s", bind)
	http.Handle("/", brokerapi.New(
		broker,
		lager.NewLogger("blacksmith-broker"),
		brokerapi.BrokerCredentials{
			Username: AuthUsername,
			Password: AuthPassword,
		}))
	http.ListenAndServe(bind, nil)
}
