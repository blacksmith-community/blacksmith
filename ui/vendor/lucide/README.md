# Lucide icons

These files are unmodified icons from the npm package `lucide-static` at version 1.65.0. Lucide is licensed under the ISC license, and its `LICENSE` file sits beside this one. Its home page is https://lucide.dev and its source is https://github.com/lucide-icons/lucide.

The package tarball is https://registry.npmjs.org/lucide-static/-/lucide-static-1.65.0.tgz. We copied only the icons the console uses into `icons/`, one file per icon, byte for byte.

The console never reads these files at run time. The three sort arrows are also embedded as data URIs in `ui/blacksmith.css`, where each one is marked by a comment naming its icon, and a test compares them to these files. `ui/icons.js` holds the same markup in one map and exposes `blacksmithIcon(name, options)`, and a test fails if the map drifts from these files. To add an icon, copy its file from the package into `icons/` and regenerate the map entry in `ui/icons.js`.
