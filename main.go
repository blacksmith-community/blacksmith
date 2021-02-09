package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

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
	if config.Debug {
		Debugging = true
	}

	l := Logger.Wrap("*")

	bind := fmt.Sprintf("%s:%s", config.Broker.BindIP, config.Broker.Port)
	l.Info("broker will listen on %s", bind)

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

	_ = vault.EnsureVaultV2()

	bosh, err := gogobosh.NewClient(&gogobosh.Config{
		BOSHAddress:       config.BOSH.Address,
		Username:          config.BOSH.Username,
		Password:          config.BOSH.Password,
		HttpClient:        http.DefaultClient,
		SkipSslValidation: config.BOSH.SkipSslValidation,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to authenticate to BOSH: %s\n", err)
		os.Exit(2)
	}

	if config.BOSH.CloudConfig != "" {
		l.Info("updating cloud-config...")
		err = bosh.UpdateCloudConfig(config.BOSH.CloudConfig)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to update CLOUD-CONFIG: %s\ncloud-config:\n%s\n", err, config.BOSH.CloudConfig)
			os.Exit(2)
		}
	}

	if config.BOSH.Releases != nil {
		rr, err := bosh.GetReleases()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to retrieve RELEASES list: %s\n", err)
			os.Exit(2)
		}
		have := make(map[string]bool)
		for _, r := range rr {
			for _, v := range r.ReleaseVersions {
				have[r.Name+"/"+v.Version] = true
			}
		}

		l.Info("uploading releases...")
		for _, r := range config.BOSH.Releases {
			if have[r.Name+"/"+r.Version] {
				l.Info("skipping %s/%s (already uploaded)", r.Name, r.Version)
				continue
			}
			task, err := bosh.UploadRelease(r.URL, r.SHA1)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nFailed to upload RELEASE (%s) sha1 [%s]: %s\n", err, r.URL, r.SHA1)
				os.Exit(2)
			}
			l.Info("uploading release %s/%s [sha1 %s] in BOSH task %d, from %s", r.Name, r.Version, r.SHA1, task.ID, r.URL)
		}
	}

	if config.BOSH.Stemcells != nil {
		ss, err := bosh.GetStemcells()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to retrieve STEMCELLS list: %s\n", err)
			os.Exit(2)
		}
		have := make(map[string]bool)
		for _, sc := range ss {
			have[sc.Name+"/"+sc.Version] = true
		}

		l.Info("uploading stemcells...")
		for _, sc := range config.BOSH.Stemcells {
			if have[sc.Name+"/"+sc.Version] {
				l.Info("skipping %s/%s (already uploaded)", sc.Name, sc.Version)
				continue
			}
			task, err := bosh.UploadStemcell(sc.URL, sc.SHA1)
			if err != nil {
				fmt.Fprintf(os.Stderr, "\nFailed to upload STEMCELL (%s) sha1 [%s]: %s\n", err, sc.URL, sc.SHA1)
				os.Exit(2)
			}
			l.Info("uploading stemcell %s/%s [sha1 %s] in BOSH task %d, from %s", sc.Name, sc.Version, sc.SHA1, task.ID, sc.URL)
		}
	}

	broker := &Broker{
		Vault: vault,
		BOSH:  bosh,
	}

	l.Info("reading services from %s", strings.Join(os.Args[3:], ", "))
	err = broker.ReadServices(os.Args[3:]...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read SERVICE directories: %s\n", err)
		os.Exit(2)
	}

	var ui http.Handler = NullHandler{}
	if config.WebRoot != "" {
		ui = http.FileServer(http.Dir(config.WebRoot))
	}
	http.Handle("/", &API{
		Username: config.Broker.Username,
		Password: config.Broker.Password,
		WebRoot:  ui,
		Internal: &InternalApi{
			Env:    config.Env,
			Vault:  vault,
			Broker: broker,
		},
		Primary: brokerapi.New(
			broker,
			lager.NewLogger("blacksmith-broker"),
			brokerapi.BrokerCredentials{
				Username: config.Broker.Username,
				Password: config.Broker.Password,
			},
		),
	})

	l.Info("blacksmith service broker v%s starting up...", Version)
	go func() {
		err := http.ListenAndServe(bind, nil)
		if err != nil {
			l.Error("blacksmith service broker failed to start up: %s", err)
			os.Exit(2)
		}
		l.Info("shutting down blacksmith service broker")
	}()

	BoshMaintenanceLoop := time.NewTicker(1 * time.Hour)
	//TODO set task to -1 or something out here and check to make sure the cleanup is finished before you run another one
	for {
		select {
		case <-BoshMaintenanceLoop.C:
			vaultDB, err := vault.getVaultDB()
			if err != nil {
				l.Error("error grabbing vaultdb for debugging: %s", err)
			}
			l.Info("current vault db looks like: %v", vaultDB.Data)
			broker.serviceWithNoDeploymentCheck()
			task, err := bosh.Cleanup(false)
			l.Info("taskid for the bosh cleanup is %v", task.ID)
			if err != nil {
				l.Error("bosh cleanup failed to run properly: %s", err)
			}
		}
	}
}
