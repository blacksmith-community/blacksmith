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
	Shield    ShieldConfig `yaml:"shield"`
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

type ShieldConfig struct {
	Enabled  bool   `yaml:"enabled"`
	Address  string `yaml:"address"`
	Insecure bool   `yaml:"skip_ssl_validation"`

	Agent string `yaml:"agent"`

	AuthMethod string `yaml:"auth_method"` // "token", "local"
	Token      string `yaml:"token"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`

	Tenant string `yaml:"tenant"` // Full UUID or exact name
	Store  string `yaml:"store"`  // Full UUID or exact name

	Schedule string `yaml:"schedule"` // daily, weekly, daily at 11:00
	Retain   string `yaml:"retain"`   // 7d, 7w, ...

	EnabledOnTargets []string `yaml:"enabled_on_targets"` // rabbitmq, redis, ...
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
	SkipSslValidation bool         `yaml:"skip_ssl_validation"`
	Stemcells         []Uploadable `yaml:"stemcells"`
	Releases          []Uploadable `yaml:"releases"`
	CCPath            string       `yaml:"cloud-config"` // TODO: CCPath vs CloudConfig & yaml???
	CloudConfig       string
	Network           string `yaml:"network"`
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

	if c.BOSH.Network == "" {
		c.BOSH.Network = "blacksmith" // Default
	}

	if err := os.Setenv("BOSH_NETWORK", c.BOSH.Network); err != nil { // Required by manifest.go
		return Config{}, fmt.Errorf("failed to set BOSH_NETWORK environment variable: %s", err)
	}

	if err := os.Setenv("VAULT_ADDR", c.Vault.Address); err != nil {
		return Config{}, fmt.Errorf("failed to set VAULT_ADDR environment variable: %s", err)
	}

	return
}
