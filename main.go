package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/cloudfoundry-community/gogobosh"
	"github.com/pivotal-cf/brokerapi"
	"github.com/pivotal-golang/lager"
)

//Version gets edited during a release build
var Version = "(development version)"

func main() {
	showVersion := flag.Bool("v", false, "Display the version of Blacksmith")
	configPath := flag.String("c", "", "path to config")
	flag.Parse()

	if *showVersion {
		fmt.Printf("blacksmith %s\n", Version)
		os.Exit(0)
	}

	config, err := ReadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}

	bind := fmt.Sprintf(":%s", config.Broker.Port)

	vault := &Vault{
		URL:      config.Vault.Address,
		Token:    "", // will be supplied soon.
		Insecure: config.Vault.Insecure,
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
	if err = vault.Init(config.Vault.CredPath); err != nil {
		log.Fatal(err)
	}

	bosh := &gogobosh.Config{
		BOSHAddress:       config.BOSH.Address,
		Username:          config.BOSH.Username,
		Password:          config.BOSH.Password,
		HttpClient:        http.DefaultClient,
		SkipSslValidation: config.BOSH.SkipSslValidation,
	}

	broker := &Broker{
		Vault: vault,
		BOSH:  gogobosh.NewClient(bosh),
	}
	err = broker.ReadServices(os.Args[3:]...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read SERVICE directories: %s\n", err)
		os.Exit(2)
	}

	http.Handle("/b/", &InternalApi{
		Vault:    vault,
		Broker:   broker,
		Username: config.Broker.Username,
		Password: config.Broker.Password,
	})
	http.Handle("/", brokerapi.New(
		broker,
		lager.NewLogger("blacksmith-broker"),
		brokerapi.BrokerCredentials{
			Username: config.Broker.Username,
			Password: config.Broker.Password,
		}))
	http.ListenAndServe(bind, nil)
}
