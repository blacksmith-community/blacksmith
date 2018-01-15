# New Features

- Blacksmith now uploads releases to its BOSH director, if you
  want it to do so.  This can be triggered by explicitly
  configuring the release in the blacksmith configuration file.
  Forges can also now reference releases via URL and SHA1, and
  the broker will upload them before any service deployment, if
  they are missing.

# Improvements

- You can now use any operator supported by Spruce 1.14.0!

- Forges can now pull back all of the IPs for a clustered
  deployment, allowing operators to scale those clusters to any
  number of constituent VMs via service plans.

- Service records in the Management UI now highlight when you
  hover, so you don't go cross-eyed finding the links to click on.

# Bug Fixes

- The Management UI now properly shows service limits.

- Deprovisioning services no longer uses the `--force` mechanism,
  so it no longer panics, and we properly handle our bookkeeping.

- Service limits are now displayed properly in the Services
  section of the Management UI.  Previously, they were always
  rendered as "infinite"
