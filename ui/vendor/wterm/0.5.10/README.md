# wterm 0.5.10

The SSH terminal in the console runs on wterm (https://wterm.dev), a terminal emulator with a Zig core compiled to WebAssembly and a DOM renderer. wterm is Apache-2.0 licensed, and `LICENSE` here is the copy from the npm packages.

These files come from `@wterm/dom` 0.5.10 and `@wterm/core` 0.5.10, published on npm.

| Package | Tarball | npm integrity |
|---------|---------|---------------|
| `@wterm/dom` 0.5.10 | https://registry.npmjs.org/@wterm/dom/-/dom-0.5.10.tgz | `sha512-Tcdig0gqVgqpoWB/i9pp4z3+ZI2Rf5MOeCy3geOuRvGSSXMGgeF6xcA/PD667tCe8Te47ldVeKYsM92uUgijCg==` |
| `@wterm/core` 0.5.10 | https://registry.npmjs.org/@wterm/core/-/core-0.5.10.tgz | `sha512-kVzgs094y/9z/w2IVaYUd75F+bUFa14cn7uhec0g9QDI1kalh8W7Ft6ckOER478gncsPrf7a9oRf1bAOe3ERgQ==` |

| File | Source | SHA-256 |
|------|--------|---------|
| `wterm.min.js` | Built from `@wterm/dom`, which pulls in `@wterm/core` | `8c7d90ea8320207495c532112e56f6d9b6141794d22fb0ffeec2ce2ec60f6a69` |
| `terminal.css` | `@wterm/dom` `src/terminal.css`, unmodified | `01be4dbfb085f9367315a4e38eb14b370cb4e00b2e60a483dd1ec4a5139a0f66` |
| `wterm.wasm` | `@wterm/core` `wasm/wterm.wasm`, unmodified | `df60f205a71419868802b0f42ec9306e8ed5dbaa1baf17b2765ab16405b76ddc` |

The SHA-256 of `wterm.wasm` matches the file inside the `@wterm/core` tarball.

## How the bundle is built

The packages are ESM only, and the console loads plain scripts without a bundler. We therefore bundle `WTerm` with bun into one minified IIFE that assigns `window.WTermLib = { WTerm, version }`, and `index.html` loads it with an ordinary `<script>` tag.

The console passes `wasmUrl: '/vendor/wterm/0.5.10/wterm.wasm'`, so the browser fetches the core as its own file. A versioned path means an upgrade never meets a stale cached module. With a custom `wasmUrl`, wterm never needs its embedded base64 copy of the binary, so the build replaces the loader's `import("./wasm-inline.js")` with a rejected promise. The bundle then carries no second copy of the WASM, and the build fails if the base64 form of the WASM header shows up in it.

To rebuild, run this from the repository root, with bun 1.4.3 or later:

```sh
scripts/vendor-wterm.sh
```

The script downloads both tarballs into a temporary directory, checks them against the npm integrity values above, writes the entry file and the bun build script there, builds the bundle, and copies `wterm.min.js`, `terminal.css`, `wterm.wasm`, and `LICENSE` here. It prints the SHA-256 of each copied file, which should match the table.

## Upgrading

Change `VERSION` and both integrity values in `scripts/vendor-wterm.sh`, and run it. It writes a new `ui/vendor/wterm/<version>/` directory. Then update the paths in `ui/index.html` and the `wasmUrl` in `ui/blacksmith.js`, update this README, and remove the old directory.
