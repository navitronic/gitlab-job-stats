# gitlab-job-stats

A Go tool that generates a CI reliability report for a GitLab project. It shows
pipeline and job success rates over a time window, with infrastructure failures
distinguished from code failures.

## Requirements

- [Go](https://go.dev) 1.21+
- [`glab`](https://gitlab.com/gitlab-org/cli) — authenticated (`glab auth login`)
- macOS or Linux

## Usage

```sh
go run . [group/project] [--period 1d|1w|1m] [--json]
```

### Arguments

| Argument | Description |
| --- | --- |
| `group/project` | GitLab project path. Inferred from `git remote get-url origin` when omitted. |
| `--period` | Time window: `1d` (1 day), `1w` (1 week), `1m` (1 month). Default: `1w`. |
| `--json` | Output structured JSON instead of the terminal report. |

### Examples

```sh
# Report for the last week, project inferred from current git repo
go run .

# Explicit project, last 24 hours
go run . mygroup/myproject --period 1d

# Last month, JSON output
go run . mygroup/myproject --period 1m --json | jq .
```

## Sample output

```
  GitLab Pipeline Reliability Report
  Project : mygroup/myproject
  Period  : 1w  (since 2026-07-22T00:00:00Z)
  ────────────────────────────────────────

  PIPELINES
  Total                  120
  Success                95
  Failed                 25
  Success rate           79.2%

  ────────────────────────────────────────

  JOBS
  Total                  840
  Success                790
  Failed                 50
  Success rate           94.0%

  JOB FAILURE BREAKDOWN
  Code failures          38
  Infra failures         12

  ────────────────────────────────────────

  TOP FAILING JOBS
  Job name                                 Type       Failures
  --------                                 ----       --------
  rspec                                    code       15
  build-docker                             infra      8
  lint                                     code       7
```

## Caching

Job lists for completed pipelines are cached permanently in
`~/.cache/gitlab-job-stats/<group_project>/`. On subsequent runs only
in-progress or uncached pipelines hit the API, making re-runs over the same
period much faster.

## Failure classification

Job failures are classified using GitLab's native `failure_reason` field.

**Infrastructure** (runner/system problems, not the developer's code):

- `runner_system_failure`
- `stuck_or_timeout_failure`
- `runner_unsupported`
- `scheduler_failure`
- `data_integrity_failure`
- `unknown_failure` _(classified as infra but flagged as ambiguous in the report)_

**Code** (the job ran and the code or tests caused it to fail):

- `script_failure`
- `job_execution_timeout`
- `missing_dependency_failure`
- `api_failure`
- Any unrecognised value _(defaults to code — errs toward actionability)_

## Notes

- All branches are included; there is no per-branch filter.
- Retried jobs: only the most recent attempt per job name per pipeline is counted.
- Large projects with many pipelines will result in more API calls. GitLab rate
  limiting will surface naturally through `glab` error messages.
