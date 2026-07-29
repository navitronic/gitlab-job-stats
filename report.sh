#!/usr/bin/env bash
set -euo pipefail

# ---------------------------------------------------------------------------
# GitLab Pipeline Reliability Report
# Usage: ./report.sh [group/project] [--period 1d|1w|1m] [--json]
# ---------------------------------------------------------------------------

PERIOD="1w"
JSON_MODE=false
PROJECT=""

# ---------------------------------------------------------------------------
# Failure reason classification
# ---------------------------------------------------------------------------
classify_failure() {
  local reason="$1"
  case "$reason" in
    runner_system_failure|stuck_or_timeout_failure|runner_unsupported|\
    scheduler_failure|data_integrity_failure|unknown_failure)
      echo "infra"
      ;;
    *)
      echo "code"
      ;;
  esac
}

# ---------------------------------------------------------------------------
# Argument parsing
# ---------------------------------------------------------------------------
parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --period)
        PERIOD="$2"
        shift 2
        ;;
      --period=*)
        PERIOD="${1#*=}"
        shift
        ;;
      --json)
        JSON_MODE=true
        shift
        ;;
      --help|-h)
        echo "Usage: $0 [group/project] [--period 1d|1w|1m] [--json]"
        echo ""
        echo "  group/project   GitLab project path (inferred from git remote if omitted)"
        echo "  --period        Time window: 1d (1 day), 1w (1 week), 1m (1 month) [default: 1w]"
        echo "  --json          Output structured JSON instead of terminal report"
        exit 0
        ;;
      -*)
        echo "Error: unknown flag '$1'" >&2
        echo "Run '$0 --help' for usage." >&2
        exit 1
        ;;
      *)
        if [[ -n "$PROJECT" ]]; then
          echo "Error: unexpected argument '$1'" >&2
          exit 1
        fi
        PROJECT="$1"
        shift
        ;;
    esac
  done

  case "$PERIOD" in
    1d|1w|1m) ;;
    *)
      echo "Error: --period must be one of: 1d, 1w, 1m (got '$PERIOD')" >&2
      exit 1
      ;;
  esac
}

# ---------------------------------------------------------------------------
# Resolve project from git remote when not provided
# ---------------------------------------------------------------------------
resolve_project() {
  if [[ -n "$PROJECT" ]]; then
    return
  fi

  local remote_url
  if ! remote_url=$(git remote get-url origin 2>/dev/null); then
    echo "Error: no project argument given and not in a git repository (or no 'origin' remote)." >&2
    exit 1
  fi

  # SSH: git@gitlab.com:group/project.git
  # HTTPS: https://gitlab.com/group/project.git
  PROJECT=$(echo "$remote_url" \
    | sed -E 's|.*[:/]([^/]+/[^/]+)(\.git)?$|\1|' \
    | sed 's|\.git$||')

  if [[ -z "$PROJECT" || "$PROJECT" == "$remote_url" ]]; then
    echo "Error: could not parse GitLab project from remote URL: $remote_url" >&2
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# Dependency checks
# ---------------------------------------------------------------------------
check_dependencies() {
  if ! command -v glab &>/dev/null; then
    echo "Error: 'glab' not found. Install from https://gitlab.com/gitlab-org/cli" >&2
    exit 1
  fi

  if ! command -v jq &>/dev/null; then
    echo "Error: 'jq' not found." >&2
    if [[ "$(uname)" == "Darwin" ]]; then
      echo "  Install with: brew install jq" >&2
    else
      echo "  Install with: sudo apt-get install jq  (or your distro's package manager)" >&2
    fi
    exit 1
  fi

  if ! glab auth status &>/dev/null; then
    echo "Error: glab is not authenticated. Run: glab auth login" >&2
    exit 1
  fi
}

# ---------------------------------------------------------------------------
# Calculate the ISO 8601 'since' timestamp for the requested period
# ---------------------------------------------------------------------------
calc_since() {
  local period="$1"
  if [[ "$(uname)" == "Darwin" ]]; then
    case "$period" in
      1d) date -u -v-1d +"%Y-%m-%dT%H:%M:%SZ" ;;
      1w) date -u -v-7d +"%Y-%m-%dT%H:%M:%SZ" ;;
      1m) date -u -v-1m +"%Y-%m-%dT%H:%M:%SZ" ;;
    esac
  else
    case "$period" in
      1d) date -u -d "1 day ago"   +"%Y-%m-%dT%H:%M:%SZ" ;;
      1w) date -u -d "7 days ago"  +"%Y-%m-%dT%H:%M:%SZ" ;;
      1m) date -u -d "1 month ago" +"%Y-%m-%dT%H:%M:%SZ" ;;
    esac
  fi
}

# ---------------------------------------------------------------------------
# URL-encode the project path (replace / with %2F)
# ---------------------------------------------------------------------------
url_encode_project() {
  echo "$1" | sed 's|/|%2F|g'
}

# ---------------------------------------------------------------------------
# Fetch all pipelines updated after $since, across all branches, paginated
# Writes a JSON array to stdout
# ---------------------------------------------------------------------------
fetch_pipelines() {
  local project_encoded="$1"
  local since="$2"
  local page=1
  local all_pipelines="[]"

  while true; do
    [[ "$JSON_MODE" == false ]] && printf "\r  Fetching pipelines… page %-3s" "$page" >&2

    local response
    response=$(glab api "projects/${project_encoded}/pipelines?per_page=100&page=${page}&updated_after=${since}" 2>&1) || {
      echo "" >&2
      echo "Error fetching pipelines (page $page): $response" >&2
      exit 1
    }

    local count
    count=$(echo "$response" | jq 'length')

    if [[ "$count" -eq 0 ]]; then
      break
    fi

    all_pipelines=$(echo "$all_pipelines $response" | jq -s '.[0] + .[1]')
    page=$((page + 1))
  done

  [[ "$JSON_MODE" == false ]] && echo "" >&2

  echo "$all_pipelines"
}

# ---------------------------------------------------------------------------
# Fetch all jobs for a single pipeline ID, paginated
# Writes a JSON array to stdout
# ---------------------------------------------------------------------------
fetch_pipeline_jobs() {
  local project_encoded="$1"
  local pipeline_id="$2"
  local page=1
  local all_jobs="[]"

  while true; do
    local response
    response=$(glab api "projects/${project_encoded}/pipelines/${pipeline_id}/jobs?per_page=100&page=${page}" 2>&1) || {
      echo "Error fetching jobs for pipeline $pipeline_id (page $page): $response" >&2
      exit 1
    }

    local count
    count=$(echo "$response" | jq 'length')

    if [[ "$count" -eq 0 ]]; then
      break
    fi

    all_jobs=$(echo "$all_jobs $response" | jq -s '.[0] + .[1]')
    page=$((page + 1))
  done

  echo "$all_jobs"
}

# ---------------------------------------------------------------------------
# Fetch all jobs across all pipelines
# Writes a JSON array (one object per job) to stdout
# ---------------------------------------------------------------------------
fetch_jobs() {
  local project_encoded="$1"
  local pipeline_ids_json="$2"  # JSON array of pipeline IDs

  local all_jobs="[]"
  local total current=0
  total=$(echo "$pipeline_ids_json" | jq 'length')

  while IFS= read -r pipeline_id; do
    current=$((current + 1))
    if [[ "$JSON_MODE" == false ]]; then
      printf "\r  Fetching jobs… pipeline %-6s of %-6s (id: %s)" \
        "$current" "$total" "$pipeline_id" >&2
    fi

    local jobs
    jobs=$(fetch_pipeline_jobs "$project_encoded" "$pipeline_id")
    local job_count
    job_count=$(echo "$jobs" | jq 'length')
    all_jobs=$(echo "$all_jobs $jobs" | jq -s '.[0] + .[1]')

    if [[ "$JSON_MODE" == false ]]; then
      local running_total
      running_total=$(echo "$all_jobs" | jq 'length')
      printf " — %s job(s) fetched, %s total so far" "$job_count" "$running_total" >&2
    fi
  done < <(echo "$pipeline_ids_json" | jq -r '.[]')

  [[ "$JSON_MODE" == false ]] && echo "" >&2

  echo "$all_jobs"
}

# ---------------------------------------------------------------------------
# Aggregate pipeline and job data into a summary JSON object
# ---------------------------------------------------------------------------
aggregate() {
  local pipelines_json="$1"
  local jobs_json="$2"
  local project="$3"
  local period="$4"
  local since="$5"

  # Pipeline counts
  local total_pipelines pipeline_success pipeline_failed pipeline_other
  total_pipelines=$(echo "$pipelines_json" | jq 'length')
  pipeline_success=$(echo "$pipelines_json" | jq '[.[] | select(.status == "success")] | length')
  pipeline_failed=$(echo "$pipelines_json"  | jq '[.[] | select(.status == "failed")]  | length')
  pipeline_other=$((total_pipelines - pipeline_success - pipeline_failed))

  local pipeline_rate="N/A"
  if [[ "$total_pipelines" -gt 0 ]]; then
    pipeline_rate=$(awk "BEGIN { printf \"%.1f\", ($pipeline_success / $total_pipelines) * 100 }")
  fi

  # Job counts
  local total_jobs job_success job_failed
  total_jobs=$(echo "$jobs_json"   | jq 'length')
  job_success=$(echo "$jobs_json"  | jq '[.[] | select(.status == "success")] | length')
  job_failed=$(echo "$jobs_json"   | jq '[.[] | select(.status == "failed")]  | length')

  # Classify failed jobs
  local code_failures=0 infra_failures=0 unknown_failures=0

  while IFS= read -r reason; do
    local category
    category=$(classify_failure "$reason")
    if [[ "$category" == "infra" ]]; then
      infra_failures=$((infra_failures + 1))
      if [[ "$reason" == "unknown_failure" ]]; then
        unknown_failures=$((unknown_failures + 1))
      fi
    else
      code_failures=$((code_failures + 1))
    fi
  done < <(echo "$jobs_json" | jq -r '.[] | select(.status == "failed") | .failure_reason // "script_failure"')

  # Top failing jobs: deduplicate retries by taking latest attempt per (pipeline_id, job name)
  # then count failures by job name
  local top_failing_jobs
  top_failing_jobs=$(echo "$jobs_json" | jq '
    [
      group_by(.pipeline.id, .name) |
      .[] |
      sort_by(.id) |
      last
    ] |
    [.[] | select(.status == "failed")] |
    group_by(.name) |
    map({
      name: .[0].name,
      failures: length,
      failure_reason: (.[0].failure_reason // "script_failure")
    }) |
    sort_by(-.failures) |
    .[0:10]
  ')

  # Add classification to top failing jobs
  local top_failing_with_type
  top_failing_with_type=$(echo "$top_failing_jobs" | jq --arg classify_script "$(declare -f classify_failure)" '
    map(. + {
      type: (
        if (.failure_reason | test("runner_system_failure|stuck_or_timeout_failure|runner_unsupported|scheduler_failure|data_integrity_failure|unknown_failure"))
        then "infra"
        else "code"
        end
      )
    }) |
    map(del(.failure_reason))
  ')

  local job_rate="N/A"
  if [[ "$total_jobs" -gt 0 ]]; then
    job_rate=$(awk "BEGIN { printf \"%.1f\", ($job_success / $total_jobs) * 100 }")
  fi

  jq -n \
    --arg project       "$project" \
    --arg period        "$period" \
    --arg since         "$since" \
    --argjson total_p   "$total_pipelines" \
    --argjson succ_p    "$pipeline_success" \
    --argjson fail_p    "$pipeline_failed" \
    --argjson other_p   "$pipeline_other" \
    --arg rate_p        "${pipeline_rate}" \
    --argjson total_j   "$total_jobs" \
    --argjson succ_j    "$job_success" \
    --argjson fail_j    "$job_failed" \
    --arg rate_j        "${job_rate}" \
    --argjson code_f    "$code_failures" \
    --argjson infra_f   "$infra_failures" \
    --argjson unknown_f "$unknown_failures" \
    --argjson top       "$top_failing_with_type" \
    '{
      project:  $project,
      period:   $period,
      since:    $since,
      pipelines: {
        total:        $total_p,
        success:      $succ_p,
        failed:       $fail_p,
        other:        $other_p,
        success_rate: ($rate_p + "%")
      },
      jobs: {
        total:        $total_j,
        success:      $succ_j,
        failed:       $fail_j,
        success_rate: ($rate_j + "%"),
        failure_breakdown: {
          code:              $code_f,
          infra:             $infra_f,
          unknown_failure_count: $unknown_f
        },
        top_failing_jobs: $top
      }
    }'
}

# ---------------------------------------------------------------------------
# Terminal report renderer
# ---------------------------------------------------------------------------
render_terminal() {
  local data="$1"

  local project period since
  project=$(echo "$data" | jq -r '.project')
  period=$(echo "$data"  | jq -r '.period')
  since=$(echo "$data"   | jq -r '.since')

  local p_total p_success p_failed p_other p_rate
  p_total=$(echo "$data"   | jq -r '.pipelines.total')
  p_success=$(echo "$data" | jq -r '.pipelines.success')
  p_failed=$(echo "$data"  | jq -r '.pipelines.failed')
  p_other=$(echo "$data"   | jq -r '.pipelines.other')
  p_rate=$(echo "$data"    | jq -r '.pipelines.success_rate')

  local j_total j_success j_failed j_rate
  j_total=$(echo "$data"   | jq -r '.jobs.total')
  j_success=$(echo "$data" | jq -r '.jobs.success')
  j_failed=$(echo "$data"  | jq -r '.jobs.failed')
  j_rate=$(echo "$data"    | jq -r '.jobs.success_rate')

  local code_f infra_f unknown_f
  code_f=$(echo "$data"    | jq -r '.jobs.failure_breakdown.code')
  infra_f=$(echo "$data"   | jq -r '.jobs.failure_breakdown.infra')
  unknown_f=$(echo "$data" | jq -r '.jobs.failure_breakdown.unknown_failure_count')

  local sep="────────────────────────────────────────"

  echo ""
  echo "  GitLab Pipeline Reliability Report"
  echo "  Project : $project"
  echo "  Period  : $period  (since $since)"
  echo "  $sep"
  echo ""
  echo "  PIPELINES"
  printf "  %-20s %s\n" "Total"   "$p_total"
  printf "  %-20s %s\n" "Success" "$p_success"
  printf "  %-20s %s\n" "Failed"  "$p_failed"
  if [[ "$p_other" -gt 0 ]]; then
    printf "  %-20s %s\n" "Other (skipped…)" "$p_other"
  fi
  printf "  %-20s %s\n" "Success rate" "$p_rate"
  echo ""
  echo "  $sep"
  echo ""
  echo "  JOBS"
  printf "  %-20s %s\n" "Total"   "$j_total"
  printf "  %-20s %s\n" "Success" "$j_success"
  printf "  %-20s %s\n" "Failed"  "$j_failed"
  printf "  %-20s %s\n" "Success rate" "$j_rate"
  echo ""
  echo "  JOB FAILURE BREAKDOWN"
  printf "  %-20s %s\n" "Code failures"  "$code_f"
  printf "  %-20s %s\n" "Infra failures" "$infra_f"

  if [[ "$unknown_f" -gt 0 ]]; then
    echo ""
    echo "  * $unknown_f job(s) failed with 'unknown_failure' — classified as infra"
    echo "    but the root cause is ambiguous. Investigate runner logs for those jobs."
  fi

  # Top failing jobs
  local top_count
  top_count=$(echo "$data" | jq '.jobs.top_failing_jobs | length')

  if [[ "$top_count" -gt 0 ]]; then
    echo ""
    echo "  $sep"
    echo ""
    echo "  TOP FAILING JOBS"
    printf "  %-40s %-10s %-6s\n" "Job name" "Type" "Failures"
    printf "  %-40s %-10s %-6s\n" "--------" "----" "--------"

    while IFS=$'\t' read -r name type failures; do
      printf "  %-40s %-10s %-6s\n" "$name" "$type" "$failures"
    done < <(echo "$data" | jq -r '.jobs.top_failing_jobs[] | [.name, .type, (.failures | tostring)] | @tsv')
  fi

  echo ""
  echo "  $sep"
  echo ""
}

# ---------------------------------------------------------------------------
# JSON renderer
# ---------------------------------------------------------------------------
render_json() {
  local data="$1"
  echo "$data" | jq .
}

# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------
main() {
  parse_args "$@"
  check_dependencies
  resolve_project

  local project_encoded
  local since
  project_encoded=$(url_encode_project "$PROJECT")
  since=$(calc_since "$PERIOD")

  if [[ "$JSON_MODE" == false ]]; then
    echo "Fetching pipelines for $PROJECT (period: $PERIOD, since: $since)…" >&2
  fi

  local pipelines
  pipelines=$(fetch_pipelines "$project_encoded" "$since")

  local pipeline_count
  pipeline_count=$(echo "$pipelines" | jq 'length')

  if [[ "$JSON_MODE" == false ]]; then
    echo "Found $pipeline_count pipeline(s)." >&2
  fi

  local pipeline_ids
  local jobs
  pipeline_ids=$(echo "$pipelines" | jq '[.[].id]')
  jobs=$(fetch_jobs "$project_encoded" "$pipeline_ids")

  if [[ "$JSON_MODE" == false ]]; then
    local job_count
    job_count=$(echo "$jobs" | jq 'length')
    echo "Found $job_count job(s). Generating report…" >&2
  fi

  local summary
  summary=$(aggregate "$pipelines" "$jobs" "$PROJECT" "$PERIOD" "$since")

  if [[ "$JSON_MODE" == true ]]; then
    render_json "$summary"
  else
    render_terminal "$summary"
  fi
}

main "$@"
