# Obedience Corp Official Marketplace

The official Festival Installer marketplace (`official-obey`): first-party Festival stack products and plugins.

- `obey-marketplace.json` — marketplace manifest the installer clones and parses.
- `index.json` — package/channel index.
- `packages/<namespace>/<name>/obey-package.json` — per-package release manifests.
- `packages/<namespace>/<name>/obey-package.json.sig` — detached Ed25519 signatures.
- `keys/` — public marketplace metadata verification keys.

Products and plugins here are consumed by `festival marketplace add/refresh/list`,
`festival install`, and `festival update`.

Package manifests are stored as canonical JSON and signed with the private key
held in the `FESTIVAL_METADATA_SIGNING_KEY` repository secret. Pull requests
verify every package manifest against the committed public key.

After changing a package manifest, run the **Sign metadata** workflow. It
canonicalizes every package manifest, verifies the resulting signatures, and
opens a pull request containing only the signed metadata changes.
