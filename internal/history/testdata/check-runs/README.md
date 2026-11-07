# check-runs

GitHub's answer to `GET /repos/{owner}/{repo}/commits/{sha}/check-runs?check_name=costroid&filter=all&per_page=100`, recorded on 2026-11-07 from a private test repository with `gh api`. Its one check run is the one `costroid report` posted for workflow run 42371097132 on 2026-11-07.

`go run ./scripts/record scrub` cut the answer to its allowlist, and `go run ./scripts/record verify` passed on it. The scrub dropped every URL, the check run's summary, the App's owner and the pull requests it named. It replaced the ids: the check run is 90000001, its commit `0000000000000000000000000000000000000001`, the workflow run 80000001, and the commit `costroid.yml` was read at `0000000000000000000000000000000000000002`.

The figures are the recorded run's own: USD 0.1309884 at list, measured, over 19 rows.
