# Blacksmith

Building production grade services for Cloud Foundry

Stay tuned!

## Running the tests

The tests start a real Vault server as a subprocess, so they need `bao` (OpenBao), or `vault`, on your `PATH`, or the path to either one in the `BLACKSMITH_TEST_VAULT_BIN` environment variable. A missing binary fails the tests rather than skipping them, and the error tells you how to fix it.
