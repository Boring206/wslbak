# Releasing

## Set up once

What a release relies on, for checking it or for doing it again after a move.

1. **The repository is public.** Provenance for the zips and for the npm package needs it.
2. **The package exists on npm.** The first version was published by hand: `npm run build`, then
   `npm publish` in a real terminal, which asks you to approve in the browser.
3. **The workflow may publish to npm.** `npm trust list wslbak` shows whether a trusted publisher is
   set. To set it: `npm trust github wslbak --file release.yml --repository Boring206/wslbak --allow-publish`
   (or on npmjs.com: the package → Settings → Trusted Publisher → GitHub Actions). Then tell the
   workflow: `gh variable set NPM_TRUSTED_PUBLISHER --body yes`. Without that variable the workflow
   makes the GitHub Release and leaves npm alone.
4. **Private vulnerability reporting is on**: repository Settings → Code security.
5. **The scoop bucket** is the repository `Boring206/scoop-bucket`. Its own workflow keeps it up to
   date.
6. **A fork of `microsoft/winget-pkgs`** in your account, for the pull requests to winget.
   `scripts/winget-pr.sh` makes it when it is missing.

## Every release

1. Set the version in `package.json` and add a section to `CHANGELOG.md`.
2. `npm test`, `npm run e2e`, and start the `e2e` workflow on GitHub.
3. Commit, then tag and push the tag: `git tag v0.1.1 && git push origin v0.1.1`.
   The `release` workflow tests, builds, creates the GitHub Release with the zips, `SHA256SUMS`
   and `package-manifests.zip`, and publishes to npm.
4. **scoop**: the bucket's workflow copies the manifest from the newest release once a day. To have
   it at once: `gh workflow run bucket -R Boring206/scoop-bucket`. GitHub switches a daily workflow
   off after 60 days without a commit in its repository; `gh workflow enable bucket -R Boring206/scoop-bucket`
   switches it back on.
5. **Check the installations**: `gh workflow run installs -f version=0.1.1`. It installs that
   version from npm, from the zip on the Releases page, from the scoop bucket, and with winget from
   the manifest files in the release. `-f ways="zip scoop winget"` leaves one out.
6. **winget**: `scripts/winget-pr.sh 0.1.1 <id of that run>` opens the pull request to
   `microsoft/winget-pkgs`. Once it is merged, `gh workflow run installs -f version=0.1.1 -f winget=source`
   installs from winget the way users do.

## Code signing

The executables are not signed. Windows SmartScreen therefore warns about a downloaded zip, and
Smart App Control blocks the program. A certificate costs money or needs an application (Azure
Trusted Signing; SignPath Foundation for open-source projects). Once there is one, signing
belongs in the `build` job of `release.yml`, before the files are zipped.
