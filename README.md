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

- The **Sign metadata** workflow runs on every same-repository pull request.
  A `gate` job verifies without the private key. If signatures already
  match, signing is skipped and the `signing` environment is not requested.
  If they do not, `sign` waits for approval in that environment, then
  canonicalizes, signs, and pushes onto the pull request branch.
- After a successful `sign`, the workflow dispatches **Metadata**
  (`workflow_dispatch`) against the same branch. That is what records the
  required `verify` check on the signed commit. Do not approve the
  `github-actions[bot]` pull_request runs that appear as `action_required`
  after the signature push: they have no jobs and are not the required
  check. The one intended click is the `signing` environment review.
- **Sign metadata** can also be run manually from the Actions tab against any
  branch (also gated by the same environment approval when signing is
  needed). There is no push-to-`main` trigger: `main` is protected and only
  accepts pull requests, so there is nothing for a push-triggered backstop
  to do.

The **Metadata** workflow verifies every signed document on every pull
request, on `workflow_dispatch`, and again on every push to `main` that
touches a signed file, `tools/metadata/**`, `go.mod`, `go.sum`, or the
workflow itself. Its `verify` check is required on `main`, so a pull
request cannot merge without it passing.

To change a manifest by hand, edit it, open a pull request, and let the
workflow canonicalize and sign it. Do not commit a signature you generated
locally: the release key is not distributed.

Note on trust: signing a document requires two things together. First, a
same-repository branch: GitHub builds the **Sign metadata** workflow from
the pull request's own branch, so a fork can never reach the signing secret
(the workflow also checks this explicitly). Second, approval of the
`signing` GitHub Environment by its required reviewer before
`FESTIVAL_METADATA_SIGNING_KEY` is exposed to any job step. That reviewer
is configured; confirm with
`gh api repos/Obedience-Corp/marketplace/environments --jq
'.environments[].protection_rules'` if the pause-for-approval behavior
ever disappears. An empty result means the environment is unprotected.
Separately, `main` accepts only pull requests whose `verify` check has
passed; nothing, including this repository's own workflows, pushes to
`main` directly.

### What signing does not cover

A signature proves who produced a document and that it has not been altered
since. It does not prove the document is current. A party who can serve an
older commit of this repository serves validly signed older content, and a
fresh install has no prior state to compare against. Closing that gap needs a
signed monotonic timestamp and a maximum-age policy, which this repository
does not have today. Treat the signatures as an authorship and integrity
guarantee, not a freshness guarantee.
