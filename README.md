# Blacksmith

Blacksmith is a Cloud Foundry service broker that provisions on-demand service instances as BOSH deployments. Platform operators run it next to their Cloud Foundry and BOSH director, and application developers then create Redis, RabbitMQ, PostgreSQL, or Valkey instances from the marketplace with the usual `cf create-service` and `cf bind-service` commands. Blacksmith keeps each instance's credentials and metadata in Vault, and it ships with a web console where operators watch instances, VMs, and BOSH tasks, run batch upgrades, and change broker settings. It can also set up SHIELD backups for the instances it creates.

## Where to start

We deploy Blacksmith with the Genesis kit at [github.com/genesis-community/blacksmith-genesis-kit](https://github.com/genesis-community/blacksmith-genesis-kit), which builds the broker's manifest and its configuration for an environment.

Each service Blacksmith offers comes from a forge, which is a BOSH release that teaches the broker how to deploy one service. The forge's plans job runs on the broker's own VM, and the broker reads the plans from there. The forges we maintain live in these repositories.

- [redis-forge-boshrelease](https://github.com/blacksmith-community/redis-forge-boshrelease)

- [valkey-forge-boshrelease](https://github.com/blacksmith-community/valkey-forge-boshrelease)

- [rabbitmq-forge-boshrelease](https://github.com/blacksmith-community/rabbitmq-forge-boshrelease)

- [postgresql-forge-boshrelease](https://github.com/blacksmith-community/postgresql-forge-boshrelease)

The `docs/` directory holds the rest of the documentation.

- [docs/broker.md](docs/broker.md) describes the broker, its components, and its HTTP routes.

- [docs/config.md](docs/config.md) is the reference for every configuration key.

- [docs/reconciler.md](docs/reconciler.md) explains how the reconciler keeps Vault, BOSH, and Cloud Foundry in step, and how to set its interval.

- [docs/service-instance-management-workflow.md](docs/service-instance-management-workflow.md) follows an instance through its life.

- [docs/run-command.md](docs/run-command.md) covers the Run command batch operation.

- [docs/tls.md](docs/tls.md) covers the broker's TLS settings.

- [docs/vm-monitor.md](docs/vm-monitor.md) covers VM health monitoring.

## Running the tests

The tests start a real Vault server as a subprocess, so they need `bao` (OpenBao), or `vault`, on your `PATH`, or the path to either one in the `BLACKSMITH_TEST_VAULT_BIN` environment variable. A missing binary fails the tests rather than skipping them, and the error tells you how to fix it.
