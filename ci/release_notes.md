# Improvements

- Blacksmith now keeps track of the date/time of service
  provisioning, and displays it in the management interface.

# Bug Fixes

- The `debug` configuration option now has the same effect as
  setting the `DEBUG` environment variable; vis-a-vis debugging
  output is enabled.

- The implicit `chmod` of a Forge plan's `init` script is now
  properly using octal permissions (0755 instead of 755).

- If a Forge plan does not provide an `init` script (unlikely but
  not impossible), Blacksmith will no longer try to execute it.
  (This was encountered in development mode only, no production
  Forge exhibits this peculiar behavior...)
