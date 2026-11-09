#!/usr/bin/env bash
# Rebuild ui/vendor/wterm/<version>/ from the published npm tarballs.
#
# The console has no bundler, so this script turns the ESM-only @wterm/dom
# and @wterm/core packages into one classic-script bundle that sets
# window.WTermLib. The WASM core is served as its own file, so the build
# replaces the loader's import of the embedded base64 copy with a rejected
# promise, and the bundle never carries the binary twice.
#
# Usage: scripts/vendor-wterm.sh
# Needs bun, curl, openssl, shasum, and tar. Everything is fetched and built
# in a temporary directory outside the repository.
set -euo pipefail

VERSION=0.5.10
CORE_INTEGRITY='sha512-kVzgs094y/9z/w2IVaYUd75F+bUFa14cn7uhec0g9QDI1kalh8W7Ft6ckOER478gncsPrf7a9oRf1bAOe3ERgQ=='
DOM_INTEGRITY='sha512-Tcdig0gqVgqpoWB/i9pp4z3+ZI2Rf5MOeCy3geOuRvGSSXMGgeF6xcA/PD667tCe8Te47ldVeKYsM92uUgijCg=='

repo=$(cd "$(dirname "$0")/.." && pwd)
out="$repo/ui/vendor/wterm/$VERSION"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fetch() {
  local name=$1 integrity=$2
  local tarball="$work/$name-$VERSION.tgz"
  curl -sSfL -o "$tarball" "https://registry.npmjs.org/@wterm/$name/-/$name-$VERSION.tgz"
  local actual
  actual="sha512-$(openssl dgst -sha512 -binary "$tarball" | base64 | tr -d '\n')"
  if [ "$actual" != "$integrity" ]; then
    echo "@wterm/$name $VERSION does not match its npm integrity" >&2
    exit 1
  fi
  mkdir -p "$work/node_modules/@wterm/$name"
  tar -xzf "$tarball" -C "$work/node_modules/@wterm/$name" --strip-components=1
}

fetch core "$CORE_INTEGRITY"
fetch dom "$DOM_INTEGRITY"

cat >"$work/entry.js" <<EOF
import { WTerm } from "@wterm/dom";

globalThis.WTermLib = Object.freeze({ WTerm, version: "$VERSION" });
EOF

cat >"$work/build.mjs" <<'EOF'
const embeddedImport = 'import("./wasm-inline.js")';

const noEmbeddedWasm = {
  name: "no-embedded-wasm",
  setup(build) {
    build.onLoad({ filter: /@wterm\/core\/dist\/wasm-loading\.js$/ }, async ({ path }) => {
      const source = await Bun.file(path).text();
      if (!source.includes(embeddedImport)) {
        throw new Error("wasm-loading.js no longer imports wasm-inline.js; update the build");
      }
      const contents = source.replace(
        embeddedImport,
        'Promise.reject(new Error("[wterm] this build has no embedded WASM; pass wasmUrl"))',
      );
      return { contents, loader: "js" };
    });
  },
};

const result = await Bun.build({
  entrypoints: ["./entry.js"],
  outdir: "./out",
  naming: "wterm.min.js",
  format: "iife",
  target: "browser",
  minify: true,
  plugins: [noEmbeddedWasm],
});

if (!result.success) {
  for (const message of result.logs) console.error(message);
  process.exit(1);
}
EOF

(cd "$work" && bun build.mjs)

bundle="$work/out/wterm.min.js"
# "AGFzbQ" is the base64 form of the WASM magic number, so it only appears
# when the embedded binary was bundled.
if grep -q 'AGFzbQ\|wasm-inline' "$bundle"; then
  echo "the bundle still contains the embedded WASM" >&2
  exit 1
fi

mkdir -p "$out"
cp "$bundle" "$out/wterm.min.js"
cp "$work/node_modules/@wterm/dom/src/terminal.css" "$out/terminal.css"
cp "$work/node_modules/@wterm/core/wasm/wterm.wasm" "$out/wterm.wasm"
cp "$work/node_modules/@wterm/dom/LICENSE" "$out/LICENSE"

(cd "$out" && shasum -a 256 wterm.min.js terminal.css wterm.wasm)
