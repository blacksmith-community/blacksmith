# Improvements

- Blacksmith now creates the following files, after it has
  initiaized or unsealed the local Blacksmith Vault:

    1. `$BLACKSMITH_OPER_HOME/.saferc` for `safe` to use
    2. `$BLACKSMITH_OPER_HOME/.svtoken` for `spruce` to use
    3. `$BLACKSMITH_OPER_HOME/.vault-token` for `vault` to use

  If the `BLACKSMITH_OPER_HOME` environment variable is set.
  This helps to keep our developers from overwriting real files,
  while still allowing the BOSH release of Blacksmith to provide
  an operator-friendly environment.
