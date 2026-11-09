#!/usr/bin/env bash
# Publish the local main only after GitHub CI (Linux and macOS) passes on that
# exact commit. Use it instead of `git push origin main`:
#
#   bash scripts/push-main-checked.sh
#
# It freezes the local main commit and pushes it to the `pre-main` branch,
# leasing only that branch. It opens or reuses one draft pull request
# pre-main -> main and waits for the CI run of that commit. Then it
# fast-forwards the remote main to the same commit, only if the remote main has
# not moved. GitHub marks that pull request merged once main reaches its head,
# so expect a fresh draft pull request for each publication; one is reused only
# after a red run. On a red or cancelled run it lists the failing jobs and does
# not touch main. pre-main and the draft pull request may already have changed.
#
# Every GitHub call names the repository that the remote fetches from and pushes
# to. A pull-request run checks out GitHub's merge of pre-main into main, not
# the commit itself. A run counts only if each build job's checkout step logged
# exactly one checked-out commit and GitHub reports that commit's tree equal to
# the tree of the commit being published.
#
# Requires git and an authenticated GitHub CLI (`gh auth login`). Bash 3.2
# compatible. Pushes no tags and writes nothing outside this repository.

remote=origin
interval=${PUSH_MAIN_POLL_SECONDS:-15}
appear_tries=${PUSH_MAIN_APPEAR_TRIES:-40}

usage() {
	echo "usage: bash scripts/push-main-checked.sh [--remote NAME]" >&2
	exit 2
}

# fail is for every stop before the final push: main is untouched.
fail() {
	printf 'push-main-checked: %s\n' "$1" >&2
	printf 'This script did not push main (pre-main and the draft pull request may have changed).\n' >&2
	exit 1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--remote)
		[ $# -ge 2 ] || usage
		remote=$2
		shift 2
		;;
	*) usage ;;
	esac
done
case "$interval" in '' | *[!0-9]*) fail "PUSH_MAIN_POLL_SECONDS must be a whole number" ;; esac
case "$appear_tries" in '' | *[!0-9]* | 0) fail "PUSH_MAIN_APPEAR_TRIES must be a positive whole number" ;; esac

top=$(git rev-parse --show-toplevel 2>/dev/null) || fail "not inside a git repository"
cd "$top" || fail "cannot enter $top"
command -v gh >/dev/null 2>&1 || fail "the GitHub CLI (gh) is not installed"

# github_repo prints OWNER/REPO for a github.com URL, or nothing.
github_repo() {
	case "$1" in
	https://github.com/*) path=${1#https://github.com/} ;;
	git@github.com:*) path=${1#git@github.com:} ;;
	ssh://git@github.com/*) path=${1#ssh://git@github.com/} ;;
	*) return 0 ;;
	esac
	path=${path%/}
	path=${path%.git}
	case "$path" in */*/* | /* | */ | '') return 0 ;; */*) printf '%s\n' "$path" ;; esac
}

# One destination: the fetch and push URLs (after insteadOf rewriting) must be
# the same single URL, on github.com. Only a local, non-GitHub remote (tests)
# may name its repository with PUSH_MAIN_TEST_REPO.
fetch_url=$(git remote get-url "$remote" 2>/dev/null) || fail "no remote named $remote"
push_urls=$(git remote get-url --push --all "$remote" 2>/dev/null) || fail "cannot read the push URL of $remote"
[ "$push_urls" = "$fetch_url" ] || fail "$remote fetches from $fetch_url but pushes to $push_urls; use one URL"
repo=$(github_repo "$fetch_url")
if [ -z "$repo" ]; then
	case "$fetch_url" in
	*://* | *@*:*) fail "$remote ($fetch_url) is not a github.com repository" ;;
	esac
	repo=${PUSH_MAIN_TEST_REPO:-}
	[ -n "$repo" ] || fail "$remote ($fetch_url) is not a github.com repository"
fi
export GH_REPO="$repo" GH_HOST=github.com
gh auth status >/dev/null 2>&1 || fail "gh is not logged in; run: gh auth login"

sha=$(git rev-parse --verify --quiet 'refs/heads/main^{commit}') || fail "no local main branch"
git fetch --quiet --no-tags "$remote" refs/heads/main || fail "cannot fetch $remote main"
base=$(git rev-parse --verify --quiet 'FETCH_HEAD^{commit}') || fail "cannot read the fetched $remote main"

if [ "$sha" = "$base" ]; then
	printf 'Remote main is already at %s; nothing to push.\n' "$sha"
	exit 0
fi
git merge-base --is-ancestor "$base" "$sha" ||
	fail "local main $sha is not a fast-forward of $remote main $base; integrate first"

printf 'Checking %s on %s (remote main is %s)\n' "$sha" "$repo" "$base"

# Lease pre-main on the value seen now: absent, or whatever it currently is.
old=$(git ls-remote "$remote" refs/heads/pre-main) || fail "cannot read $remote pre-main"
old=${old%%[[:space:]]*}
git push --quiet --no-follow-tags --force-with-lease="refs/heads/pre-main:$old" \
	"$remote" "$sha:refs/heads/pre-main" || fail "pushing pre-main failed"

prs=$(gh pr list --repo "$repo" --head pre-main --base main --state open --json number,isDraft \
	--jq '.[] | "\(.number) \(.isDraft)"') || fail "cannot list pull requests"
if [ -z "$prs" ]; then
	gh pr create --repo "$repo" --draft --head pre-main --base main \
		--title "pre-main check" \
		--body "Checks the local main before it is published; created by scripts/push-main-checked.sh. Not merged through GitHub: main is fast-forwarded to the checked commit." \
		>/dev/null || fail "cannot create the draft pull request"
	prs=$(gh pr list --repo "$repo" --head pre-main --base main --state open --json number,isDraft \
		--jq '.[] | "\(.number) \(.isDraft)"') || fail "cannot read the new pull request"
fi
case "$prs" in
*"
"*) fail "more than one open pull request from pre-main; close the extras" ;;
*" true") pr=${prs%% *} ;;
'') fail "no open pull request for pre-main" ;;
*) fail "the open pull request from pre-main is not a draft; close it or mark it draft" ;;
esac
printf 'Draft pull request #%s\n' "$pr"

# A pull_request run of this commit on pre-main in this repository.
run=
tries=0
while :; do
	run=$(gh run list --repo "$repo" --workflow ci.yml --branch pre-main --event pull_request --limit 20 \
		--json databaseId,headSha --jq "[.[] | select(.headSha == \"$sha\")][0].databaseId // empty") ||
		fail "cannot list CI runs"
	[ -n "$run" ] && break
	tries=$((tries + 1))
	[ "$tries" -lt "$appear_tries" ] || fail "no CI run started for $sha"
	sleep "$interval"
done
linked=$(gh api --hostname github.com "repos/$repo/actions/runs/$run" \
	--jq "[.pull_requests[] | select(.number == $pr and .head.sha == \"$sha\" and .base.repo.url == \"https://api.github.com/repos/$repo\")] | length") ||
	fail "cannot read CI run $run"
[ "$linked" = 1 ] || fail "CI run $run is not linked to draft pull request #$pr for $sha"
printf 'Waiting for CI run %s\n' "$run"

gh run watch "$run" --repo "$repo" --exit-status --interval "$interval"
watched=$?
result=$(gh run view "$run" --repo "$repo" --json headSha,headBranch,event,conclusion \
	--jq '.headSha + " " + .headBranch + " " + .event + " " + .conclusion') ||
	fail "cannot read CI run $run"
if [ "$watched" -ne 0 ] || [ "$result" != "$sha pre-main pull_request success" ]; then
	printf 'CI run %s did not pass (%s). Failing jobs:\n' "$run" "${result##* }" >&2
	gh run view "$run" --repo "$repo" --json jobs \
		--jq '.jobs[] | select(.conclusion != "success" and .conclusion != "skipped") | "  \(.name): \(.conclusion // .status)  \(.url)"' >&2
	printf 'Logs: gh run view %s --repo %s --log-failed\n' "$run" "$repo" >&2
	fail "CI failed for $sha"
fi

passed=$(gh run view "$run" --repo "$repo" --json jobs --jq '.jobs[] | select(.conclusion == "success") | .name') ||
	fail "cannot read the jobs of CI run $run"
log=$(gh run view "$run" --repo "$repo" --log) || fail "cannot read the log of CI run $run"
# The checkout step logs `git log -1 --format=%H` and then the full commit it
# checked out; read it per build job, from that step only.
want_tree=$(git rev-parse --verify --quiet "$sha^{tree}") || fail "cannot read the tree of $sha"
# Keep these generated job names aligned with ci.yml's build matrix.
for os in ubuntu-24.04 macos-latest; do
	job="build ($os)"
	printf '%s\n' "$passed" | grep -Fxq -- "$job" || fail "CI run $run has no passing job \"$job\""
	checked=$(printf '%s\n' "$log" | awk -F '\t' -v job="$job" -v q="'" '
		$1 == job && index($2, "actions/checkout") > 0 {
			msg = $3; sub(/^[^ ]* /, "", msg); gsub(q, "", msg)
			if (want) { want = 0; if (length(msg) == 40 && msg ~ /^[0-9a-f]+$/) print msg; next }
			if (index(msg, "git log -1 --format") > 0) want = 1
		}' | sort -u)
	case "$checked" in
	'' | *"
"*) fail "CI run $run cannot vouch for $sha: job \"$job\" shows no single checked-out commit" ;;
	esac
	tree=$(gh api --hostname github.com "repos/$repo/git/commits/$checked" --jq .tree.sha) ||
		fail "cannot read the checked-out commit $checked of job \"$job\""
	[ "$tree" = "$want_tree" ] ||
		fail "CI run $run cannot vouch for $sha: job \"$job\" tested $checked, whose tree differs"
done

git fetch --quiet --no-tags "$remote" refs/heads/main || fail "cannot re-fetch $remote main"
now=$(git rev-parse --verify --quiet 'FETCH_HEAD^{commit}') || fail "cannot read the re-fetched $remote main"
[ "$now" = "$base" ] || fail "$remote main moved to $now while CI ran; run this again"

if ! git push --no-follow-tags "$remote" "$sha:refs/heads/main"; then
	# The server may have applied the update before the reply was lost: read,
	# report and do not retry.
	printf 'push-main-checked: pushing main to %s reported a failure; nothing was retried.\n' "$sha" >&2
	if seen=$(git ls-remote "$remote" refs/heads/main 2>/dev/null) && [ -n "$seen" ]; then
		seen=${seen%%[[:space:]]*}
		if [ "$seen" = "$sha" ]; then
			printf 'Remote main now equals the intended commit %s.\n' "$seen" >&2
		elif [ "$seen" = "$base" ]; then
			printf 'Remote main still equals %s, as before.\n' "$seen" >&2
		else
			printf 'Remote main now reads %s, neither commit: check it before anything else.\n' "$seen" >&2
		fi
	else
		printf 'Remote main could not be read back: its state is uncertain; check it before anything else.\n' >&2
	fi
	exit 1
fi
printf 'Pushed %s to %s main after CI run %s passed on Linux and macOS.\n' "$sha" "$repo" "$run"
