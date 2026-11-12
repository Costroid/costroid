# Costroid for Claude Code

This plugin lets Claude Code read what an agent's run on a pull request cost, and how the `costroid` check concluded. Its tools only read: none of them changes, posts or deletes anything.

It holds:

- the `costroid` skill, the same text `costroid init --skills` writes;
- `/costroid:status`, what the `costroid run` that started the agent has spent so far, its cap and what is left;
- `/costroid:result NUMBER`, the result object of the newest `costroid` check run on a pull request;
- the MCP server `costroid mcp`, with four tools that only read.

## Install

The plugin starts its server as `costroid mcp`, so the `costroid` binary must be on your `PATH`, at a release that has the `mcp` command. [Install it](https://github.com/Costroid/costroid#install) first. Then add this repository as a marketplace and install the plugin from it, in Claude Code:

    /plugin marketplace add Costroid/costroid
    /plugin install costroid@costroid

`/costroid:result` reads GitHub with `GITHUB_TOKEN`, from the environment Claude Code starts in. The plugin never asks `gh` for a token. With the GitHub CLI signed in, start Claude Code from a shell that ran:

    export GITHUB_TOKEN="$(gh auth token)"

The repository is the one `GITHUB_REPOSITORY` names, as `OWNER/NAME`. Without it, the plugin reads the GitHub repository that the git remote of the directory Claude Code started in names: `origin`, or the only remote.

## What it reads

`/costroid:status` reads the status file `costroid run` writes while the agent works. Outside such a run there is no file, and the command says so. `/costroid:result` reads the result object from the check run, never from the pull request comment. [`docs/agents.md`](https://github.com/Costroid/costroid/blob/main/docs/agents.md) describes both objects.

## In CI

`claude -p --bare` loads no plugin. [`recipes/github-claude-code.yml`](https://github.com/Costroid/costroid/blob/main/recipes/github-claude-code.yml) gives the agent the plugin's server with `--mcp-config`, from the `costroid` binary the action installs, and allows its `run_status` tool alone. `run_status` reads the status file that `costroid run` writes while the agent works.
