# Obedience Corp Official Marketplace

The official `obey-installer` marketplace (`official-obey`): first-party Festival stack products and plugins.

- `obey-marketplace.json` — marketplace manifest the installer clones and parses.
- `index.json` — package/channel index.
- `packages/<namespace>/<name>/obey-package.json` — per-package release manifests.

Products and plugins here are consumed by `obey-installer marketplace add/refresh/list` and `install/update`.
