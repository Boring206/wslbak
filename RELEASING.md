# Releasing

## Before the first release

1. **Rehearse.** On GitHub: Actions → release → Run workflow. That builds the release files and,
   without publishing anything, checks the checksums, runs the program from the zip, installs the
   packed npm package and, where the tools are there, validates the winget manifests and
   installs through scoop.
2. **Make the repository public.** Provenance for the zips and for the npm package needs it.
3. **Publish to npm once by hand**, so that the package exists:
   `npm run build && npm publish` (it asks for your second factor).
4. **Allow the workflow to publish.** On npmjs.com: the package → Settings → Trusted Publisher →
   GitHub Actions, with user `Boring206`, repository `wslbak`, workflow file `release.yml`.
   Then tell the workflow that this is done: `gh variable set NPM_TRUSTED_PUBLISHER --body yes`.
   Until that variable is set, the workflow makes the GitHub Release and leaves npm alone.
5. **Turn on private vulnerability reporting**: repository Settings → Code security.

## Every release

1. Set the version in `package.json` and add a section to `CHANGELOG.md`.
2. `npm test`, `npm run e2e`, and start the `e2e` workflow on GitHub.
3. Commit, then tag and push the tag: `git tag v0.1.0 && git push origin v0.1.0`.
   The `release` workflow tests, builds, creates the GitHub Release with the zips and
   `SHA256SUMS`, and publishes to npm.
4. The release contains `package-manifests.zip`:
   - **winget**: send the three files under `winget/` as a pull request to
     `microsoft/winget-pkgs`, in `manifests/b/Boring206/wslbak/<version>/`.
   - **scoop**: put `scoop/wslbak.json` in your bucket repository.

## Code signing

The executables are not signed. Windows SmartScreen therefore warns about a downloaded zip, and
Smart App Control blocks the program. A certificate costs money or needs an application (Azure
Trusted Signing; SignPath Foundation for open-source projects). Once there is one, signing
belongs in the `build` job of `release.yml`, before the files are zipped.
