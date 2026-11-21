#!/bin/sh
# Writes each command's help page, as the costroid binary named prints it, to
# this directory: one file a command. The pages here are those of the commit
# before help took its parts and their order, and a test holds every sentence
# of them to the page the command prints now.
#
#   git worktree add ../before 1c14655
#   (cd ../before && go build -o costroid ./cmd/costroid)
#   cmd/costroid/testdata/said/collect.sh ../before/costroid
set -eu

binary=$1
here=$(dirname "$0")
for name in demo lint init doctor run ingest-run report follow-up approve \
	reconcile export merged connect review policy validate lint-recipes \
	gateway serve keys egress version result mcp emit-otlp app; do
	"$binary" help "$name" >"$here/$name.txt"
done
