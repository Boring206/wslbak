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

## Every release

1. Set the version in `package.json` and add a section to `CHANGELOG.md`.
2. `npm test` and `npm run e2e`.
3. Commit and push main: `git push origin main`. Wait for the `test` workflow that the push starts.
   Then start the `e2e` workflow and a rehearsal of the release on that commit: `gh workflow run e2e`
   and `gh workflow run release`. Started by hand, `release` builds the same files and installs
   them without publishing anything. Tag only when all three have passed on the commit that gets
   the tag (an `e2e` job that ends with "lost communication" is the runner freezing: start it
   again).
4. Tag and push the tag: `git tag v<version> && git push origin v<version>`.
   The `release` workflow tests, builds, creates the GitHub Release with the zips, `SHA256SUMS`
   and `package-manifests.zip`, and then publishes to npm. Before it publishes it marks the
   executables as executable and checks that npm packs them so (`scripts/pack-check.mjs`): they
   come from a Windows runner through an artifact, which does not keep file modes. The same script
   checks that `README.md` is the only file npm can take for the package page.
   Publishing by hand has to be done from Linux or WSL: `npm test && npm run build`, then
   `node scripts/pack-check.mjs`, then `npm publish --ignore-scripts` (without that option npm
   builds the executables once more after the check).
   If one of the two publishing jobs fails, run it again with `gh run rerun <run id> --failed`;
   do not move or delete the tag once npm has the version.
5. **The text of the release.** GitHub writes only a link to the list of commits. Put the section of
   `CHANGELOG.md` there: cut it out into a file and `gh release edit v<version> --notes-file <file>`.
6. **scoop**: the bucket's workflow copies the manifest from the newest release once a day. To have
   it at once: `gh workflow run bucket -R Boring206/scoop-bucket`. GitHub switches a daily workflow
   off after 60 days without a commit in its repository; `gh workflow enable bucket -R Boring206/scoop-bucket`
   switches it back on.
7. **Check the installations**, a few minutes after the npm job has ended (the registry needs
   that long): `gh workflow run installs -f version=<version>`. It installs that version from npm,
   from the zip on the Releases page and from the scoop bucket.
   `-f ways="zip scoop"` checks only some of them.

## winget

wslbak has not been submitted to winget: a pull request to `microsoft/winget-pkgs` needs the
contributor to accept Microsoft's contributor licence agreement, and that was put off. What is
ready for the day it is wanted:

- Every release contains the three manifest files, in `package-manifests.zip`.
- `gh workflow run installs -f version=<version> -f ways=winget` validates them and installs from
  them on a Windows runner. That has been done for 0.1.0, whose manifests had version 1.10.0 of
  the manifest format. Since they were moved to 1.12.0 the winget on GitHub's runners (1.11) only
  validates them "with warnings" and ends with an error code, which that workflow takes for a
  failure; so this check has to be made to work again before it can be relied on.
- `scripts/winget-pr.sh <version> <id of that run>` is meant to open the pull request, from a fork
  of `microsoft/winget-pkgs` in your account. It has never been run, so expect to correct it.

## Code signing

The executables are not signed. Windows SmartScreen therefore warns about a downloaded zip, and
Smart App Control blocks the program. A certificate costs money or needs an application (Azure
Trusted Signing; SignPath Foundation for open-source projects). Once there is one, signing
belongs in the `build` job of `release.yml`, before the files are zipped.
