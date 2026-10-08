# Releasing

Releases are built **on a maintainer's machine**, and published as files of a GitHub release. GitHub doesn't build anything; it only hosts the files.

## What a release contains

| File | For |
| --- | --- |
| `syncloud_<v>_linux_<arch>.tar.gz` | everything for one server: controller, agent, synctl, install.sh, LICENSE, README |
| `synctl_<v>_<os>_<arch>.tar.gz` | the CLI for macOS and Windows |
| `syncloud-controller-linux-<arch>`, `syncloud-agent-linux-<arch>`, `synctl-<os>-<arch>` | raw binaries: what install.sh and in-place upgrades download |
| `syncloud-postgres-<tag>-linux-amd64.tar.gz` | the managed PostgreSQL images (OCI layouts). The controller loads one into its registry when a database first needs it. |
| `install.sh` | the installer: `…/releases/latest/download/install.sh` always works |
| `SHA256SUMS` | checksums of every file. install.sh and upgrades refuse anything that doesn't match. |

Architectures: linux/amd64 and linux/arm64 for servers; synctl also for macOS (amd64, arm64) and Windows (amd64). The PostgreSQL images are amd64.

## Cut a release

1. Make sure `main` is clean and green (`make vet test`). For changes to how things run on nodes, run the relevant [end-to-end tests](testing.md).
2. Add a `## [X.Y.Z] - YYYY-MM-DD` section to `CHANGELOG.md` and commit it.
3. If `images/postgres` changed, raise its tag in `internal/system/manifest.go` (`PostgresTag18 = "18-r2"`), so clusters pick up the new image.
4. Run the script:

   ```sh
   scripts/release.sh 0.2.0 --dry-run    # optional: tests and build only; check dist/
   scripts/release.sh 0.2.0
   ```

The script checks the tree, tag and changelog, runs `go vet` and `go test`, and builds `dist/` with `make release`. This builds the PostgreSQL images too, if they're missing. It then verifies the checksums and the binaries' version, tags `v0.2.0`, pushes the tag, and creates the GitHub release with the changelog section as notes.

- A version with a suffix, such as `0.3.0-rc.1`, becomes a **prerelease**. It's on the `beta` channel, not `stable`.
- `--draft` publishes a draft, to check before announcing.
- `--no-images` leaves the images out. That's for test releases only: each controller takes the images from its **own** version's release, so a cluster on such a release can't create PostgreSQL databases unless the archives are placed by hand.

You need `gh` signed in with permission to create releases, and Docker for the images.

## Versions

SynCloud follows [semantic versioning](https://semver.org). Until 1.0, minor versions may break things, and the changelog says how to upgrade.
