# Oat UI 1.0.0

These files are the unmodified `oat.min.css`, `oat.min.js`, and `LICENSE` from the npm package `@knadh/oat` at version 1.0.0, published 2026-10-18. Oat's home page is https://oat.ink, and it is MIT licensed.

No page loads these files yet. They are pinned here so the console can adopt Oat without fetching anything at build or run time.

| File | SHA-256 |
|------|---------|
| oat.min.css | 129d3c21c5af7eb7dcf1c4e6681fab467d5843a31ba602ab3635f0ce5630499b |
| oat.min.js | 237913c2d580c74b3682b15008eed60e93ed139a3ac0cd3adf0e46c62a5666b6 |

The package tarball is https://registry.npmjs.org/@knadh/oat/-/oat-1.0.0.tgz, with npm integrity `sha512-8+ZoFS08UFVgcm8CZwW9ehELvELBxxaKFGE3S+n8nOTeUplIumwhe5ov66izL4peBOmiQU6cJ9zk5Z77Vh/nDQ==`.

The GitHub release v1.0.0 at https://github.com/knadh/oat/releases has no attached build files, so we checked these against GitHub by rebuilding. The tag v1.0.0 points at commit 3b8d10eef03bc7db46dddb2ee8490d453280f02d. Running the tag's own `make css js` with esbuild 0.28.2 on a `git archive` of that commit produces `oat.min.css` and `oat.min.js` byte for byte identical to the files here. `LICENSE` matches the tag's copy, and the package's `css/` and `js/` sources match the tag's `src/css/` and `src/js/` exactly.

To update, rebuild the new tag the same way, and confirm the npm package's files match the rebuild before copying the same three files over these. Then update the version, date, commit, and checksums here.
