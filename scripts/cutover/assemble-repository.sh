#!/usr/bin/env bash
# Assemble omarchy-maclab from its two sources, on a machine with git and Go.
#
#   assemble-repository.sh [--maclab SRC] [--testing SRC] [--date DATE]
#                          [--skip-tests] DEST
#
# DEST must not exist. The result is one branch, main, with no remote:
#
#   1. maclab's history (SRC's main, as it stands)
#   2. one commit renaming the module path
#   3. omarchy-m-testing's history, authors kept
#   4. the joining commit
#
# Paths both trees have keep maclab's text in the joining commit. The other
# tree's copies, and its workflows, scripts and agent instructions, are held
# under .held/omarchy-m-testing/ for the layout change that follows.
#
# It pushes nothing. Run it again into a new DEST whenever a source moves.
# --date pins the two new commits' dates, so the same sources and the same
# identity give the same commit ids.

set -euo pipefail

MACLAB_SRC="https://github.com/chriskearney/maclab.git"
TESTING_SRC="https://github.com/maralcbr/omarchy-m-testing.git"
BRANCH="main"
OLD_MODULE="github.com/iconidentify/maclab"
NEW_MODULE="github.com/omacom/omarchy-maclab"
HELD=".held/omarchy-m-testing"
DATE=""
RUN_TESTS=1
DEST=""

die()  { printf 'error: %s\n' "$*" >&2; exit 1; }
info() { printf '%s\n' "$*"; }

while (( $# )); do
  case "$1" in
    --maclab)     MACLAB_SRC="${2:?--maclab needs a source}"; shift 2 ;;
    --testing)    TESTING_SRC="${2:?--testing needs a source}"; shift 2 ;;
    --date)       DATE="${2:?--date needs a date}"; shift 2 ;;
    --skip-tests) RUN_TESTS=0; shift ;;
    -h|--help)    sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    -*)           die "unknown option: $1" ;;
    *)            [[ -z "$DEST" ]] || die "one destination only"; DEST="$1"; shift ;;
  esac
done

[[ -n "$DEST" ]] || die "name the destination directory"
[[ ! -e "$DEST" ]] || die "$DEST exists; choose a new directory or move it away"
for tool in git go gofmt perl; do
  command -v "$tool" >/dev/null || die "$tool is not installed"
done
owner_name="${GIT_AUTHOR_NAME:-$(git config user.name || true)}"
owner_email="${GIT_AUTHOR_EMAIL:-$(git config user.email || true)}"
[[ -n "$owner_name" && -n "$owner_email" ]] || die "git has no user.name and user.email"
if [[ -n "$DATE" ]]; then
  export GIT_AUTHOR_DATE="$DATE" GIT_COMMITTER_DATE="$DATE"
fi

# A path of omarchy-m-testing's that waits under $HELD.
held() {
  case "$1" in
    AGENTS.md|CLAUDE.md|.github/*|scripts/*|docs/agents/*) return 0 ;;
  esac
  git -C "$DEST" cat-file -e "HEAD:$1" 2>/dev/null
}

authors() { git -C "$DEST" log --format='%an <%ae>' "$@" | sort -u; }

info "1/4 maclab's history from $MACLAB_SRC"
git clone --quiet --no-local --no-tags --single-branch --branch "$BRANCH" "$MACLAB_SRC" "$DEST"
git -C "$DEST" remote remove origin
maclab_tip="$(git -C "$DEST" rev-parse HEAD)"
maclab_commits="$(git -C "$DEST" rev-list --count HEAD)"
maclab_files="$(git -C "$DEST" ls-files | wc -l | tr -d ' ')"
maclab_authors="$(authors HEAD)"

info "2/4 the module path: $OLD_MODULE to $NEW_MODULE"
(
  cd "$DEST"
  git grep -lz -F "$OLD_MODULE" | xargs -0 perl -pi -e "s{\Q$OLD_MODULE\E}{$NEW_MODULE}g"
  git diff --name-only -z -- '*.go' | xargs -0 gofmt -w
  if git grep -n -F "${OLD_MODULE#github.com/}"; then
    die "the old module path is still named above"
  fi
  GOOS=linux go build ./...
  git commit --quiet -a -m "Rename the module path to $NEW_MODULE" \
    -m "The Go module was $OLD_MODULE. Nothing else changes: imports follow the path and are sorted again."
)
rename_commit="$(git -C "$DEST" rev-parse HEAD)"

info "3/4 omarchy-m-testing's history from $TESTING_SRC"
git -C "$DEST" fetch --quiet --no-tags "$TESTING_SRC" "refs/heads/$BRANCH"
testing_tip="$(git -C "$DEST" rev-parse FETCH_HEAD)"
testing_commits="$(git -C "$DEST" rev-list --count "$testing_tip")"
testing_files="$(git -C "$DEST" ls-tree -r --name-only "$testing_tip" | wc -l | tr -d ' ')"
testing_authors="$(authors "$testing_tip")"

info "4/4 the joining commit"
held_count=0
(
  cd "$DEST"
  git merge --quiet --allow-unrelated-histories -s ours --no-commit "$testing_tip" >/dev/null
  git ls-tree -r -z "$testing_tip" | while IFS= read -r -d '' entry; do
    path="${entry#*$'\t'}"
    if held "$path"; then path="$HELD/$path"; fi
    printf '%s\t%s\0' "${entry%%$'\t'*}" "$path"
  done | git update-index -z --add --index-info
  git checkout-index --all
  git commit --quiet -m "Join omarchy-m-testing's history" \
    -m "maclab's history stands at ${maclab_tip:0:12} with the module path renamed after it; omarchy-m-testing's stands at ${testing_tip:0:12}." \
    -m "Paths both trees have keep maclab's text. omarchy-m-testing's copies, its workflows, scripts and agent instructions wait under $HELD/ until the layout is settled."
)
held_count="$(git -C "$DEST" ls-files -- "$HELD" | wc -l | tr -d ' ')"

info "checking"
notes="$(mktemp)"
(( RUN_TESTS )) || echo "go test was skipped" > "$notes"
(
  cd "$DEST"
  [[ -z "$(git remote)" ]] || die "a remote is left"
  [[ -z "$(git status --porcelain)" ]] || die "the working tree is not clean"
  [[ "$(git rev-parse 'HEAD^1')" == "$rename_commit" ]] || die "the joining commit's first parent is not the rename"
  [[ "$(git rev-parse 'HEAD^2')" == "$testing_tip" ]] || die "the joining commit's second parent is not omarchy-m-testing's tip"
  [[ "$(git rev-parse 'HEAD^1^')" == "$maclab_tip" ]] || die "the rename does not follow maclab's tip"

  want=$(( maclab_commits + 1 + testing_commits + 1 ))
  got="$(git rev-list --count HEAD)"
  [[ "$got" == "$want" ]] || die "commits: $got, expected $want"

  files="$(git ls-files | wc -l | tr -d ' ')"
  [[ "$files" == $(( maclab_files + testing_files )) ]] \
    || die "files: $files, expected $maclab_files + $testing_files"

  want_authors="$(printf '%s\n%s\n%s\n' "$maclab_authors" "$testing_authors" "$owner_name <$owner_email>" | sort -u)"
  [[ "$(authors HEAD)" == "$want_authors" ]] || die "the authors differ from both sources' plus the owner"

  if git grep -n -F "${OLD_MODULE#github.com/}" -- . ":!$HELD"; then
    die "the old module path is still named above"
  fi

  # maclab builds for Linux alone until the layout change makes it build on macOS.
  GOOS=linux go vet ./...
  if (( RUN_TESTS )); then
    if [[ "$(go env GOHOSTOS)" == linux ]]; then
      go test ./...
    else
      RUN_TESTS=0
      TESTS_NOTE="go test was not run: at this commit the tests pass on Linux only, and this is $(go env GOHOSTOS)"
    fi
  fi
  [[ -z "${TESTS_NOTE:-}" ]] || printf '%s\n' "$TESTS_NOTE" > "$notes"
)

info ""
info "assembled $DEST"
info "  maclab            $maclab_commits commits to ${maclab_tip:0:12}"
info "  rename            ${rename_commit:0:12}"
info "  omarchy-m-testing $testing_commits commits to ${testing_tip:0:12}"
info "  main              $(git -C "$DEST" rev-list --count HEAD) commits to $(git -C "$DEST" rev-parse --short=12 HEAD)"
info "  authors           $(authors HEAD | wc -l | tr -d ' ')"
info "  held              $held_count files under $HELD/"
if [[ -s "$notes" ]]; then info "  $(cat "$notes")"; fi
rm -f "$notes"
