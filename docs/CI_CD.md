# CI/CD and contribution workflow

The workflow follows [tsastat](https://github.com/HelixThinker/tsastat/tree/26800ac500ed60f254e1c24f4f5f368e2d289ad5/.github/workflows): PR checks, a Go compatibility matrix, GoReleaser snapshots, tag-based releases, weekly dependency updates, and a separate static GitHub Pages deployment. Pod Rightsizer retains its Kubernetes integration checks and packages both commands.

## Branches and pull requests

- `main` is the integration and release branch. Create short-lived branches from an updated `main`, for example `codex/ci-release-alignment`.
- Open a pull request targeting `main`. The `CI passed` check must succeed before merging. This aggregate check fails if any test, kind, snapshot, website, or analysis-contract job fails or is skipped.
- Changes must go through a PR; an additional person's approval is not required. Resolve review discussions before merging. Force pushes and branch deletion are blocked on `main`.
- Use squash merge for a focused change. GitHub deletes merged head branches automatically. No persistent `develop` or release branch is needed.
- Repository rules are configured in GitHub, separately from these versioned workflows. Changes to the CI job graph must preserve the `CI passed` check name or update the rule at the same time.

## Continuous integration

`.github/workflows/ci.yml` runs on PRs to `main`, pushes to `main`, manual dispatch, and calls from the release workflow. It has read-only repository permissions and cancels superseded runs for the same workflow/ref. All workflows use the explicit `ubuntu-24.04` runner label so an `ubuntu-latest` migration cannot change the operating system unexpectedly.

| Job | Checks |
| --- | --- |
| `Go minimum` and `Go stable` | Minimum Go read from `go.mod`, plus the current stable release; `go mod tidy -diff`, `gofmt`, `go vet`, race-enabled tests with coverage, build both commands |
| `kind end-to-end` | Create an isolated cluster and prove server-side patch dry-run leaves both target and sidecar unchanged |
| `GoReleaser snapshot` | Build Linux amd64/arm64 archives; extract the amd64 archive and start its packaged Advisor UI without using source-tree files |
| `Website checks` | Validate local asset/fragment links and JavaScript syntax |
| `Analysis contract` | `bash scripts/check-contracts.sh`: compile `contracts/v1` and validate `testdata/contracts/v1` |
| `CI passed` | Require all preceding jobs to succeed |

The kind scenario validates resource patch behavior; it does not perform a full load-test/Metrics Server integration test. Coverage is collected, without an arbitrary global percentage threshold.

Weekly Dependabot updates cover Go modules and GitHub Actions. Action references are pinned to commit SHAs and updated together. Kubernetes modules (`k8s.io/*` and `sigs.k8s.io/*`) are grouped for both version and security updates: upgrading `apimachinery` or `api` independently of `client-go` can produce incompatible generated code. Kubernetes v0.37.1 requires Go 1.26; the minimum CI job follows `go.mod` automatically when a dependency update raises that requirement. Docker and user-facing build instructions must be updated alongside a change in the required Go series.

## Releases

Releases are explicit: pushing a version tag matching `v*` starts `.github/workflows/release.yml`. The tagged commit must belong to `main` history. GoReleaser validates the semantic version and generates release notes. A tag does not deploy the program into a Kubernetes cluster.

The release workflow first calls the complete CI workflow on the tagged revision. Only after it succeeds can the publication job use `contents: write`, `id-token: write`, and `attestations: write` permissions.

The release contains:

- Linux amd64 and arm64 `.tar.gz` archives, each with `pod-rightsizer`, `advisor-api`, `web/ui/`, README, and license;
- `checksums.txt`;
- GitHub artifact attestations for the archives and checksums.

From a reviewed, merged commit, choose the next unused semantic version:

```bash
git switch main
git pull --ff-only
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push origin vX.Y.Z
```

Replace `vX.Y.Z` with the actual release version. Review prereleases using a suffix such as `-rc.1`; GoReleaser determines prerelease status from the version. Do not move an already published version tag.

To check packaging without publishing:

```bash
goreleaser check
goreleaser release --snapshot --clean
# On Linux amd64:
bash scripts/check-release.sh
```

The smoke check starts only the extracted API binary and requests its static UI. It does not run an analysis or contact a Kubernetes cluster.

## GitHub Pages

The buildless site lives in `site/`. `.github/workflows/pages.yml` validates and publishes it after relevant changes reach `main`, or on manual dispatch. The `github-pages` environment is restricted to deployment from `main`. GitHub Pages must use GitHub Actions as its build source.

The expected project URL is [helixthinker.github.io/pod-rightsizer](https://helixthinker.github.io/pod-rightsizer/). Deployment waits for the workflow to be present on `main`. The public site uses sample data and has no connection to the Advisor API or any cluster.

Local checks and preview:

```bash
python3 scripts/check-site.py
node --check site/theme.js
node --check site/app.js
python3 -m http.server 8000 --directory site --bind 127.0.0.1
```

Open `http://127.0.0.1:8000/`. No JavaScript dependencies or site build step are required.

The site offers Light, Dark, and System appearance modes. System is the default and follows OS changes while the page is open. Explicit choices persist under `pod-rightsizer-theme` in local storage and synchronize across tabs. `theme.js` runs before the stylesheet to avoid flashing the wrong theme; if storage is unavailable, the controls still work for the current page. Without JavaScript, CSS follows the system appearance.

The visual hierarchy follows the tsastat site with a deeper green palette: separate background and card surfaces, restrained accents, softer dark-mode text, and consistent focus indicators. Resource labels and numerical values accompany chart colors. Keep both dark token blocks in `site/styles.css` aligned, and check both themes on narrow screens when editing the site. Reduced-motion preferences disable smooth scrolling and transitions.

## Deliberate differences from tsastat

- Keep the kind test: Kubernetes patch validation is central to this program.
- Package the Advisor UI as well as both binaries; a CLI-only archive is insufficient here.
- A release reruns the full CI suite, including race and kind checks, before publication.
- Apply the branch rules explicitly to `main`. During this setup, tsastat's named ruleset had an empty target list and the effective branch-rules endpoint returned no rules; its settings were not copied blindly.
- Require green CI and PRs, with zero mandatory approvals, as agreed for this repository.
- Keep Linux amd64/arm64 release targets. Additional local OS targets can be added after platform testing; Linux/eBPF-specific behavior from tsastat does not apply to this Go/Kubernetes application.
- Container registry publication and automatic deployment to a user cluster are separate delivery decisions. This setup publishes binary releases and the project website.
