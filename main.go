package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

type config struct {
	project  string
	period   string
	jsonMode bool
	since    time.Time
}

func main() {
	cfg, err := parseArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\nRun with --help for usage.\n", err)
		os.Exit(1)
	}

	if err := checkDependencies(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}

	if cfg.project == "" {
		cfg.project, err = projectFromGitRemote()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %s\n", err)
			os.Exit(1)
		}
	}

	cfg.since = calcSince(cfg.period)

	if !cfg.jsonMode {
		fmt.Fprintf(os.Stderr, "Fetching pipelines for %s (period: %s, since: %s)…\n",
			cfg.project, cfg.period, cfg.since.Format(time.RFC3339))
	}

	client := newGitLabClient(cfg.project)

	pipelines, err := client.fetchPipelines(cfg.since, cfg.jsonMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching pipelines: %s\n", err)
		os.Exit(1)
	}

	if !cfg.jsonMode {
		fmt.Fprintf(os.Stderr, "Found %d pipeline(s).\n", len(pipelines))
	}

	cache, err := newCache(cfg.project)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: cache unavailable (%s), continuing without cache.\n", err)
	}

	jobs, err := client.fetchAllJobs(pipelines, cache, cfg.jsonMode)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching jobs: %s\n", err)
		os.Exit(1)
	}

	if !cfg.jsonMode {
		fmt.Fprintf(os.Stderr, "Found %d job(s). Generating report…\n", len(jobs))
	}

	report := aggregate(cfg.project, cfg.period, cfg.since, pipelines, jobs)

	if cfg.jsonMode {
		renderJSON(report)
	} else {
		renderTerminal(report)
	}
}

func parseArgs(args []string) (config, error) {
	cfg := config{period: "1w"}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			fmt.Print(`Usage: go run . [group/project] [--period 1d|1w|1m] [--json]

  group/project   GitLab project path (inferred from git remote if omitted)
  --period        Time window: 1d, 1w, 1m (default: 1w)
  --json          Output structured JSON
`)
			os.Exit(0)
		case "--json":
			cfg.jsonMode = true
		case "--period":
			if i+1 >= len(args) {
				return cfg, fmt.Errorf("--period requires a value")
			}
			i++
			cfg.period = args[i]
		default:
			if strings.HasPrefix(args[i], "--period=") {
				cfg.period = strings.TrimPrefix(args[i], "--period=")
			} else if strings.HasPrefix(args[i], "-") {
				return cfg, fmt.Errorf("unknown flag %q", args[i])
			} else {
				if cfg.project != "" {
					return cfg, fmt.Errorf("unexpected argument %q", args[i])
				}
				cfg.project = args[i]
			}
		}
	}

	switch cfg.period {
	case "1d", "1w", "1m":
	default:
		return cfg, fmt.Errorf("--period must be one of: 1d, 1w, 1m (got %q)", cfg.period)
	}

	return cfg, nil
}

func calcSince(period string) time.Time {
	now := time.Now().UTC()
	switch period {
	case "1d":
		return now.AddDate(0, 0, -1)
	case "1w":
		return now.AddDate(0, 0, -7)
	case "1m":
		return now.AddDate(0, -1, 0)
	}
	return now.AddDate(0, 0, -7)
}

func checkDependencies() error {
	if _, err := exec.LookPath("glab"); err != nil {
		return fmt.Errorf("'glab' not found. Install from https://gitlab.com/gitlab-org/cli")
	}
	out, err := exec.Command("glab", "auth", "status").CombinedOutput()
	if err != nil {
		return fmt.Errorf("glab is not authenticated. Run: glab auth login\n%s", string(out))
	}
	return nil
}

func projectFromGitRemote() (string, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("no project argument given and not in a git repository (or no 'origin' remote)")
	}
	url := strings.TrimSpace(string(out))

	// git@gitlab.com:group/project.git  or  https://gitlab.com/group/project.git
	// Extract the last two path components.
	url = strings.TrimSuffix(url, ".git")
	url = strings.ReplaceAll(url, ":", "/")
	parts := strings.Split(url, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("could not parse GitLab project from remote URL: %s", url)
	}
	return strings.Join(parts[len(parts)-2:], "/"), nil
}
