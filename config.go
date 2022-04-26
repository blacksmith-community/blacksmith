package main

import (
	"fmt"
	"io/ioutil"
	"os"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Broker    BrokerConfig `yaml:"broker"`
	Vault     VaultConfig  `yaml:"vault"`
	BOSH      BOSHConfig   `yaml:"bosh"`
	Debug     bool         `yaml:"debug"`
	WebRoot   string       `yaml:"web-root"`
	Env       string       `yaml:"env"`
	Shareable bool         `yaml:"shareable"`
}

type BrokerConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Port     string `yaml:"port"`
	BindIP   string `yaml:"bind_ip"`
}

type VaultConfig struct {
	Address  string `yaml:"address"`
	Token    string `yaml:"token"`
	Insecure bool   `yaml:"skip_ssl_validation"`
	CredPath string `yaml:"credentials"`
}

type Uploadable struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
	URL     string `yaml:"url"`
	SHA1    string `yaml:"sha1"`
}

type BOSHConfig struct {
	Address           string       `yaml:"address"`
	Username          string       `yaml:"username"`
	Password          string       `yaml:"password"`
	ClientID          string       `yaml:"client_id"`
	ClientSecret      string       `yaml:"client_secret"`
	SkipSslValidation bool         `yaml:"skip_ssl_validation"`
	Stemcells         []Uploadable `yaml:"stemcells"`
	Releases          []Uploadable `yaml:"releases"`
	CCPath            string       `yaml:"cloud-config"`
	CloudConfig       string
}

func ReadConfig(path string) (c Config, err error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return
	}

	err = yaml.Unmarshal(b, &c)

	if err != nil {
		return
	}

	if c.Broker.Username == "" {
		c.Broker.Username = "blacksmith"
	}

	if c.Broker.Password == "" {
		c.Broker.Password = "blacksmith"
	}

	if c.Broker.Port == "" {
		c.Broker.Port = "3000"
	}

	if c.Vault.Address == "" {
		return c, fmt.Errorf("Vault Address is not set")
	}

	if c.BOSH.Address == "" {
		return c, fmt.Errorf("BOSH Address is not set")
	}

	if c.BOSH.Username == "" {
		return c, fmt.Errorf("BOSH Username is not set")
	}

	if c.BOSH.Password == "" {
		return c, fmt.Errorf("BOSH Password is not set")
	}

	if c.BOSH.CCPath != "" {
		/* cloud-config provided; try to read it. */
		b, err := ioutil.ReadFile(c.BOSH.CCPath)
		if err != nil {
			return c, fmt.Errorf("BOSH cloud-config file '%s': %s", c.BOSH.CCPath, err)
		}
		c.BOSH.CloudConfig = string(b)
	}

	os.Setenv("VAULT_ADDR", c.Vault.Address)

	return
}
