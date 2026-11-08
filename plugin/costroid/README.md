# Costroid for Claude Code

This plugin lets Claude Code read what an agent's run on a pull request cost, and how the `costroid` check concluded. Its tools only read: none of them changes, posts or deletes anything.

It holds:

- the `costroid` skill, the same text `costroid init --skills` writes;
- `/costroid:status`, what the `costroid run` that started the agent has spent so far, its cap and what is left;
- `/costroid:result NUMBER`, the result object of the newest `costroid` check run on a pull request;
- the MCP server `costroid mcp`, with four tools that only read.

## Install

The plugin starts its server as `costroid mcp`, so the `costroid` binary must be on your `PATH`, at a release that has the `mcp` command. [Install it](https://github.com/Costroid/costroid#install) first. Then, in Claude Code:

    /plugin install costroid --marketplace Costroid/costroid

`/costroid:result` reads GitHub with `GITHUB_TOKEN`, and the repository from `GITHUB_REPOSITORY`, in the environment Claude Code starts in.

## What it reads

`/costroid:status` reads the status file `costroid run` writes while the agent works. Outside such a run there is no file, and the command says so. `/costroid:result` reads the result object from the check run, never from the pull request comment. [`docs/agents.md`](https://github.com/Costroid/costroid/blob/main/docs/agents.md) describes both objects.

## In CI

`claude -p --bare` loads no plugin. [`recipes/github-claude-code.yml`](https://github.com/Costroid/costroid/blob/main/recipes/github-claude-code.yml) gives the agent the plugin's server with `--mcp-config`, from the `costroid` binary the action installs, and allows its `run_status` tool alone. `run_status` reads the status file that `costroid run` writes while the agent works.
