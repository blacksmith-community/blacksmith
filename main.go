package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/cloudfoundry-community/gogobosh"
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

	ok := true
	vault := &Vault{
		URL:      os.Getenv("VAULT_ADDR"),
		Token:    os.Getenv("VAULT_TOKEN"),
		Insecure: os.Getenv("VAULT_SKIP_VERIFY") != "",
	}
	if vault.URL == "" {
		fmt.Fprintf(os.Stderr, "No VAULT_ADDR environment variable set!\n")
		ok = false
	}
	if vault.Token == "" {
		fmt.Fprintf(os.Stderr, "No VAULT_TOKEN environment variable set!\n")
		ok = false
	}
	vault.HTTP = &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: vault.Insecure,
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			req.Header.Add("X-Vault-Token", vault.Token)
			return nil
		},
	}

	bosh := &gogobosh.Config{
		BOSHAddress:       os.Getenv("BOSH_ADDRESS"),
		Username:          os.Getenv("BOSH_USERNAME"),
		Password:          os.Getenv("BOSH_PASSWORD"),
		HttpClient:        http.DefaultClient,
		SkipSslValidation: os.Getenv("BOSH_SKIP_SSL") != "",
	}
	if bosh.BOSHAddress == "" {
		fmt.Fprintf(os.Stderr, "No BOSH_ADDRESS environment variable set!\n")
		ok = false
	}
	if bosh.Username == "" {
		fmt.Fprintf(os.Stderr, "No BOSH_USERNAME environment variable set!\n")
		ok = false
	}
	if bosh.Password == "" {
		fmt.Fprintf(os.Stderr, "No BOSH_PASSWORD environment variable set!\n")
		ok = false
	}

	if !ok {
		os.Exit(1)
	}
	broker := &Broker{
		Vault: vault,
		BOSH:  gogobosh.NewClient(bosh),
	}
	fmt.Printf("found %v\n", os.Args)
	err := broker.ReadServices(os.Args[1:]...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read SERVICE directories: %s\n", err)
		os.Exit(2)
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
