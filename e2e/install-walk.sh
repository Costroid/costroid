#!/usr/bin/env bash
# Walks the README's first screen and the install page as a newcomer does, in
# a fresh container per image, and fails when the commands that fail differ
# from e2e/testdata/install-walk.want.
#
# Each of ubuntu:24.04, debian:12 and alpine:3.20 runs alone, with 1 GiB of
# memory, and its container is removed when its walk ends. In each:
#
#  setup     As root, the newcomer adds curl, which no image holds.
#  one-step  As a user with no privilege, the block under "Install" of
#            README.md, line by line. This tree's scripts/install.sh, stamped
#            as the release workflow stamps it for the release the recipes
#            pin, replaces the copy the first line downloads. The script
#            downloads from the public release. Then what the script prints:
#            the line that adds its directory to PATH, which holds for the
#            rest of the phase as it does in a newcomer's shell, and the
#            command its Next: line names. Then the commands under "First
#            commands" of README.md that the Next: line did not name.
#  by-hand   The block under "The binary" of docs/install.md, line by line,
#            in a new directory.
#  binary    This tree's binary, built for the same release, in
#            /usr/local/bin. In a new git repository: the commands under
#            "First commands" of README.md, the first command under "Try it on
#            a pull request", which writes the workflow, and costroid doctor.
#            After each, the command its Next: line names, unless the phase
#            typed it already.
#
# Then, on the machine the walk runs on, the line under "From the image" of
# docs/install.md, with an empty Docker configuration, so no sign-in. The
# walk removes the image afterwards unless the machine held it before.
#
# A line is typed as a newcomer pastes it, without its comment: one command
# at a time, and the walk goes on when one fails. The user, git, the
# repository and the binary in /usr/local/bin are the walk's own setup, and no
# line counts them. A Next: line names a command, then what it does, which
# opens with one of the verbs in next_command.
#
# It needs Docker, Go and the network: Docker Hub, each image's package index,
# the public release and its image on ghcr.io, and the cosign release the
# script names.
set -euo pipefail

cd "$(dirname "$0")/.."
want=e2e/testdata/install-walk.want
images=(ubuntu:24.04 debian:12 alpine:3.20)
user=newcomer
home=/home/$user

version=$(sed -n 's/^  COSTROID_VERSION: //p' recipes/gitlab-ci.yml)
commit=$(sed -n 's/^  COSTROID_COMMIT: //p' recipes/gitlab-ci.yml)
if [[ ! $version || ! $commit ]]; then
  echo "install-walk: recipes/gitlab-ci.yml names no release" >&2
  exit 1
fi

work=$(mktemp -d)
container=
pulled=
session=
followed=()
cleanup() {
  if [[ $container ]]; then
    docker rm --force "$container" >/dev/null 2>&1 || true
  fi
  if [[ $pulled ]]; then
    docker image rm "$pulled" >/dev/null 2>&1 || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

sed -e "s/^version=\$/version=$version/" -e "s/^commit=\$/commit=$commit/" scripts/install.sh >"$work/costroid-install.sh"
CGO_ENABLED=0 GOOS=linux go build -trimpath \
  -ldflags "-s -w -X main.version=$version -X main.commit=$commit" \
  -o "$work/costroid" ./cmd/costroid
chmod 0755 "$work" "$work/costroid"
chmod 0644 "$work/costroid-install.sh"

# block prints the first sh block under the level-two heading $2 of the page
# $1, one command a line, each without its comment.
block() {
  awk -v heading="## $2" '
    $0 == heading { section = 1; next }
    section && /^## / { exit }
    section && $0 == "```sh" { code = 1; next }
    code && $0 == "```" { exit }
    code && sub(/\\$/, "") { joined = joined $0; next }
    code { line = joined $0; joined = ""; sub(/ +# .*$/, "", line); print line }
  ' "$1"
}

# next_command prints the command the last Next: line of file $1 names.
next_command() {
  awk '
    /^Next: / { line = $0 }
    END {
      n = split(line, word, " ")
      for (i = 2; i <= n; i++) {
        if (word[i] ~ /^(prints|writes|checks|gives)$/) {
          print command
          exit
        }
        command = command (command == "" ? "" : " ") word[i]
      }
    }
  ' "$1"
}

# setup runs a command as $1 in directory $2 that the walk needs and no
# newcomer types.
setup() {
  docker exec --user "$1" --workdir "$2" "$container" sh -c "$3" >"$work/setup.log" 2>&1 || {
    cat "$work/setup.log" >&2
    echo "install-walk: the walk's own setup failed on $image: $3" >&2
    exit 1
  }
}

# record counts one typed command of phase $1, which ended with code $3, and
# lists it as failed unless the code is 0. Its output is in $work/out.
record() {
  local phase=$1 line=$2 code=$3
  count=$((count + 1))
  printf '  %-8s $ %s\n' "$phase" "$line"
  if ((code != 0)); then
    failures=$((failures + 1))
    printf '    exit %d:\n' "$code"
    awk '{ line[NR] = $0 } END { for (i = 1; i <= NR; i++) if (i <= 2 || i > NR - 2) print "      " line[i] }' "$work/out"
    printf '%s %s $ %s\n' "$image" "$phase" "${line//$version/<version>}" >>"$work/failed"
  fi
}

# typed runs one command as $2 in directory $3, as phase $1, in a shell that
# has run $session, and records it.
typed() {
  local phase=$1 as=$2 directory=$3 line=$4 code=0
  docker exec --user "$as" --workdir "$directory" --env "HOME=$(home_of "$as")" "$container" sh -c "$session$line" >"$work/out" 2>&1 || code=$?
  record "$phase" "$line" "$code"
}

# follow types command $1 in the repository, then the command its Next: line
# names, and so on, and stops at a command the phase typed already.
follow() {
  local command=$1 seen
  while [[ $command ]]; do
    for seen in "${followed[@]}"; do
      if [[ $seen == "$command" ]]; then
        return
      fi
    done
    followed+=("$command")
    typed binary "$user" "$home/repository" "$command"
    command=$(next_command "$work/out")
  done
}

home_of() {
  if [[ $1 == root ]]; then echo /root; else echo "$home"; fi
}

walk() {
  docker pull --quiet "$image" >/dev/null
  container=$(docker run --detach --rm --memory 1g --volume "$work:/walk:ro" "$image" sleep 1800)
  count=0 failures=0

  setup root / "useradd --create-home $user 2>/dev/null || adduser -D $user"
  if [[ $image == alpine:* ]]; then
    typed setup root / "apk add --no-cache curl"
  else
    typed setup root / "apt-get update"
    typed setup root / "apt-get install -y curl"
  fi

  local line next named path_line swapped=
  while IFS= read -r line; do
    typed one-step "$user" "$home" "$line"
    if [[ $line == curl*costroid-install.sh ]]; then
      setup "$user" "$home" "cp /walk/costroid-install.sh costroid-install.sh"
      swapped=1
    fi
  done < <(block README.md "Install")
  if [[ ! $swapped ]]; then
    echo "install-walk: the block under \"Install\" of README.md downloads no costroid-install.sh" >&2
    exit 1
  fi
  next=$(next_command "$work/out")
  path_line=$(sed -n 's/^  \(export PATH=.*\)$/\1/p' "$work/out")
  if [[ $path_line ]]; then
    typed one-step "$user" "$home" "$path_line"
    session=$path_line$'\n'
  fi
  named=
  if [[ $next ]]; then
    typed one-step "$user" "$home" "$next"
    # The README writes the same command without the binary's directory.
    named=${next%% *}
    named=${named##*/}${next#"${next%% *}"}
  fi
  while IFS= read -r line; do
    if [[ $line != "$named" ]]; then
      typed one-step "$user" "$home" "$line"
    fi
  done < <(block README.md "First commands")
  session=

  setup "$user" "$home" "mkdir by-hand"
  while IFS= read -r line; do
    typed by-hand "$user" "$home/by-hand" "$line"
  done < <(block docs/install.md "The binary")

  setup root / "cp /walk/costroid /usr/local/bin/costroid"
  if [[ $image == alpine:* ]]; then
    setup root / "apk add --no-cache git"
  else
    setup root / "apt-get install -y git"
  fi
  setup "$user" "$home" "git init --quiet repository"
  followed=()
  while IFS= read -r line; do
    follow "$line"
  done < <(block README.md "First commands")
  IFS= read -r line < <(block README.md "Try it on a pull request")
  follow "$line"
  follow "costroid doctor"

  docker rm --force "$container" >/dev/null
  container=
  echo "$image: $count commands typed, $failures failed"
}

# from_the_image types the install page's image line on this machine.
from_the_image() {
  local line name code=0
  line=$(block docs/install.md "From the image")
  name=$(grep --only-matching 'ghcr\.io/[^ ]*' <<<"$line") || {
    echo "install-walk: the block under \"From the image\" of docs/install.md names no image on ghcr.io" >&2
    exit 1
  }
  count=0 failures=0
  if ! docker image inspect "$name" >/dev/null 2>&1; then
    pulled=$name
  fi
  mkdir "$work/docker"
  DOCKER_CONFIG=$work/docker sh -c "$line" >"$work/out" 2>&1 || code=$?
  record image "$line" "$code"
  if [[ $pulled ]]; then
    docker image rm "$pulled" >/dev/null 2>&1 || true
    pulled=
  fi
  echo "$image: $count commands typed, $failures failed"
}

: >"$work/failed"
for image in "${images[@]}"; do
  echo "$image"
  walk
done
image=host
echo "$image"
from_the_image

if ! diff -u <(grep -v -e '^#' -e '^$' "$want" | LC_ALL=C sort) <(LC_ALL=C sort "$work/failed"); then
  echo "install-walk: the commands that failed differ from $want, shown above as + for a new failure and - for one that no longer fails" >&2
  exit 1
fi
echo "install-walk: the commands that failed are the ones $want lists"
