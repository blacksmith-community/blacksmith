# New Features

- Blacksmith now uploads releases to its BOSH director, if you
  want it to do so.  This can be triggered by explicitly
  configuring the release in the blacksmith configuration file.
  Forges can also now reference releases via URL and SHA1, and
  the broker will upload them before any service deployment, if
  they are missing.

# Bug Fixes

- The Management UI now properly shows service limits.

- Deprovisioning services no longer uses the `--force` mechanism,
  so it no longer panics, and we properly handle our bookkeeping.
