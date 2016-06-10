package main

import (
	"fmt"
	"io/ioutil"
	"os"

	"gopkg.in/yaml.v2"
)

type Config struct {
	Broker BrokerConfig `yaml:"broker"`
	Vault  VaultConfig  `yaml:"vault"`
	BOSH   BOSHConfig   `yaml:"bosh"`
	Debug  bool         `yaml:"debug"`
}

type BrokerConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	Port     string `yaml:"port"`
}

type VaultConfig struct {
	Address  string `yaml:"address"`
	Token    string `yaml:"token"`
	Insecure bool   `yaml:"skip_ssl_validation"`
}

type BOSHConfig struct {
	Address           string `yaml:"address"`
	Username          string `yaml:"username"`
	Password          string `yaml:"password"`
	SkipSslValidation bool   `yaml:"skip_ssl_validation"`
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

	if c.Vault.Token == "" {
		return c, fmt.Errorf("Vault Token is not set")
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

	os.Setenv("VAULT_ADDR", c.Vault.Address)
	os.Setenv("VAULT_TOKEN", c.Vault.Token)

	return
}
