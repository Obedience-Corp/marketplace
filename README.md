# Obedience Corp Official Marketplace

The official Festival Installer marketplace (`official-obey`): first-party Festival stack products and plugins.

- `obey-marketplace.json` - marketplace manifest the installer clones and
  parses. **Signed. Machine-maintained: do not hand-edit.**
- `index.json` - package/channel index. **Signed. Machine-maintained: do not
  hand-edit.**
- `packages/<namespace>/<name>/obey-package.json` - per-package release
  manifests. **Signed. Machine-maintained: do not hand-edit.**
- `<file>.sig` - detached Ed25519 signature next to each signed file above.
- `keys/` - public marketplace metadata verification keys. Not signed; this is
  the published half of the trust root.

Products and plugins here are consumed by `festival marketplace add/refresh/list`,
`festival install`, and `festival update`.

## Signing contract

Every signed document is stored on disk already canonical (RFC 8785), and its
detached `.sig` covers the exact bytes of the stored file. Nothing
canonicalizes on the verification path. This means the files look like one
long line of JSON, which is deliberate: they are generated and signed by
machine, and a hand edit will invalidate the signature and break installs for
everyone using the official marketplace.

The private key lives in exactly one place, the `FESTIVAL_METADATA_SIGNING_KEY`
repository secret in this repository. Its public half is committed at
`keys/obedience-marketplace-2026-01.pub` and compiled into the `festival`
binary, so a released hub verifies against the key it shipped with rather
than a key it downloads.

Signing happens automatically:

- The **Sign metadata** workflow runs on every pull request. It canonicalizes
  and signs all three document classes and, if that changes anything, pushes
  the signatures onto the pull request branch itself. If nothing changed it
  exits without pushing, which is also what stops it from looping on the
  push it just made.
- On a push to `main`, the same workflow runs as a backstop and pushes the
  signed commit directly onto `main`. It does not open a pull request:
  Obedience-Corp does not allow GitHub Actions to create or approve pull
  requests, and `main` has no branch protection today, so a direct push is
  the working path.
- **Sign metadata** can also be run manually from the Actions tab against any
  branch.

The **Metadata** workflow verifies every signed document, but only runs on a
pull request or a push to `main` that touches a signed file,
`tools/metadata/**`, `go.mod`, `go.sum`, or the workflow itself; it does not
run on unrelated changes.

To change a manifest by hand, edit it, open a pull request, and let the
workflow canonicalize and sign it. Do not commit a signature you generated
locally: the release key is not distributed.

Note on trust: a same-repository pull request runs with the same
`FESTIVAL_METADATA_SIGNING_KEY` secret the **Sign metadata** workflow always
has, because GitHub builds that workflow from the pull request's own branch.
Anyone who can open a pull request against this repository can change
`tools/metadata` or the workflow file itself and have it run with that key.
Treat push access to this repository as equivalent to holding the signing
key.

### What signing does not cover

A signature proves who produced a document and that it has not been altered
since. It does not prove the document is current. A party who can serve an
older commit of this repository serves validly signed older content, and a
fresh install has no prior state to compare against. Closing that gap needs a
signed monotonic timestamp and a maximum-age policy, which this repository
does not have today. Treat the signatures as an authorship and integrity
guarantee, not a freshness guarantee.
