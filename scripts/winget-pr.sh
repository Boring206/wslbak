#!/usr/bin/env bash
# Opens the pull request that offers a released version to winget (microsoft/winget-pkgs).
#
#   scripts/winget-pr.sh <version> <id of a successful run of the installs workflow for it>
#
# The three manifest files are taken from the release (package-manifests.zip), where the
# release workflow wrote them together with the checksums of the zips. They become one commit
# on a new branch of <your account>/winget-pkgs, made through the GitHub API, so that very
# large repository is never cloned.
#
# The checklist of the pull request says the manifest was validated and installed from. That
# is what the given run of the installs workflow did, on a Windows runner; the script refuses
# a run that was not for this version or did not succeed.
#
# Needs gh (signed in), jq and unzip. Microsoft's bot asks a first-time contributor to accept
# their contributor licence agreement in a comment on the pull request; that is yours to do.
set -euo pipefail

version="${1:-}"
run="${2:-}"
if ! [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ && "$run" =~ ^[0-9]+$ ]]; then
	sed -n '2,4p' "$0" >&2
	exit 2
fi

repo=Boring206/wslbak
id=Boring206.wslbak
upstream=microsoft/winget-pkgs
folder="manifests/b/Boring206/wslbak"
me="$(gh api user -q .login)"
fork="$me/winget-pkgs"
branch="$id-$version"

# The run names itself "installs <version>: <ways>, winget from <manifest or source>".
checked="$(gh run view "$run" -R "$repo" --json workflowName,displayTitle,conclusion,url)"
if ! [[ "$(jq -r '.workflowName + "|" + .conclusion + "|" + .displayTitle' <<<"$checked")" =~ ^installs\|success\|installs\ ${version//./\\.}:\ .*winget.*,\ winget\ from\ manifest$ ]]; then
	echo "Run $run is not a successful run of the installs workflow for $version with winget from the manifest:" >&2
	jq . <<<"$checked" >&2
	exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
gh release download "v$version" -R "$repo" -p package-manifests.zip -D "$work"
unzip -q "$work/package-manifests.zip" -d "$work"
for file in "$work"/winget/*.yaml; do
	grep -q "^PackageVersion: $version\$" "$file" || { echo "$(basename "$file") in the release is not for $version" >&2; exit 1; }
	grep -q '^ManifestVersion: 1\.12\.0$' "$file" || { echo "$(basename "$file") is not a 1.12.0 manifest; the text of the pull request says it is" >&2; exit 1; }
done

open="$(gh pr list -R "$upstream" --state open --search "$id in:title" --json url -q '.[].url')"
if [ -n "$open" ]; then
	echo "There is an open pull request for $id already: $open" >&2
	exit 1
fi

if gh api "repos/$upstream/contents/$folder" >/dev/null 2>&1; then
	title="Update: $id to $version"
else
	title="New package: $id version $version"
fi

gh repo view "$fork" >/dev/null 2>&1 || gh repo fork "$upstream" --clone=false
gh repo sync "$fork" --source "$upstream" --branch master
base="$(gh api "repos/$fork/git/ref/heads/master" -q .object.sha)"
tree="$(for file in "$work"/winget/*.yaml; do
	jq -n --arg path "$folder/$version/$(basename "$file")" --rawfile content "$file" '{path: $path, mode: "100644", type: "blob", content: $content}'
done | jq -s --arg base "$(gh api "repos/$fork/git/commits/$base" -q .tree.sha)" '{base_tree: $base, tree: .}' |
	gh api "repos/$fork/git/trees" --input - -q .sha)"
commit="$(jq -n --arg message "$title" --arg tree "$tree" --arg parent "$base" '{message: $message, tree: $tree, parents: [$parent]}' |
	gh api "repos/$fork/git/commits" --input - -q .sha)"
gh api "repos/$fork/git/refs" -f ref="refs/heads/$branch" -f sha="$commit" >/dev/null

gh pr create -R "$upstream" --base master --head "$me:$branch" --title "$title" --body "## 📖 Description

$title.

wslbak makes scheduled, verified backups of a WSL distro without stopping it. The package is a zip with two portable executables, built by the release workflow of https://github.com/$repo.

## ✅ Checklist

- [ ] Signed the [Contributor License Agreement](https://cla.opensource.microsoft.com)
- [ ] Linked to an issue (if applicable)

## 📦 Manifest Checklist

- [x] Checked that there aren't other open [pull requests](https://github.com/microsoft/winget-pkgs/pulls) for the same manifest update/change
- [x] This PR only modifies one (1) manifest
- [x] Validated manifest locally with \`winget validate --manifest <path>\`
- [x] Tested manifest locally with \`winget install --manifest <path>\`
- [x] Manifest conforms to the [1.12 schema](https://github.com/microsoft/winget-pkgs/tree/master/doc/manifest/schema/1.12.0)

Validating and the test installation were done on a GitHub Actions Windows runner, not on a desktop PC: $(jq -r .url <<<"$checked")${WINGET_PR_FOOTER:+

$WINGET_PR_FOOTER}"
