package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Pipeline represents a GitLab CI pipeline.
type Pipeline struct {
	ID        int    `json:"id"`
	Status    string `json:"status"`
	Ref       string `json:"ref"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Job represents a GitLab CI job.
type Job struct {
	ID            int    `json:"id"`
	Name          string `json:"name"`
	Status        string `json:"status"`
	FailureReason string `json:"failure_reason"`
	PipelineID    int    // populated after fetch
}

type gitLabClient struct {
	project        string
	projectEncoded string
}

func newGitLabClient(project string) *gitLabClient {
	return &gitLabClient{
		project:        project,
		projectEncoded: url.PathEscape(project),
	}
}

// fetchPipelines returns all pipelines updated after since, across all branches.
func (c *gitLabClient) fetchPipelines(since time.Time, quiet bool) ([]Pipeline, error) {
	sinceStr := since.Format(time.RFC3339)
	var all []Pipeline
	page := 1

	for {
		if !quiet {
			fmt.Fprintf(os.Stderr, "\r  Fetching pipelines… page %-3d", page)
		}

		endpoint := fmt.Sprintf(
			"projects/%s/pipelines?per_page=100&page=%d&updated_after=%s",
			c.projectEncoded, page, url.QueryEscape(sinceStr),
		)

		data, err := glabAPI(endpoint)
		if err != nil {
			if !quiet {
				fmt.Fprintln(os.Stderr)
			}
			return nil, fmt.Errorf("page %d: %w", page, err)
		}

		var batch []Pipeline
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, fmt.Errorf("page %d: parse error: %w", page, err)
		}

		if len(batch) == 0 {
			break
		}

		all = append(all, batch...)
		page++
	}

	if !quiet {
		fmt.Fprintln(os.Stderr)
	}

	return all, nil
}

// fetchAllJobs fetches jobs for every pipeline, using the cache for completed ones.
func (c *gitLabClient) fetchAllJobs(pipelines []Pipeline, cache *Cache, quiet bool) ([]Job, error) {
	total := len(pipelines)
	var all []Job

	for i, p := range pipelines {
		if !quiet {
			fmt.Fprintf(os.Stderr, "\r  Fetching jobs… pipeline %d of %d (id: %d)       ",
				i+1, total, p.ID)
		}

		jobs, cached, err := c.jobsForPipeline(p, cache)
		if err != nil {
			if !quiet {
				fmt.Fprintln(os.Stderr)
			}
			return nil, fmt.Errorf("pipeline %d: %w", p.ID, err)
		}

		all = append(all, jobs...)

		if !quiet {
			src := "api"
			if cached {
				src = "cache"
			}
			fmt.Fprintf(os.Stderr, "— %d job(s) [%s], %d total so far", len(jobs), src, len(all))
		}
	}

	if !quiet {
		fmt.Fprintln(os.Stderr)
	}

	return all, nil
}

// jobsForPipeline returns jobs for one pipeline, using cache when available.
// Only completed pipelines are cached permanently.
func (c *gitLabClient) jobsForPipeline(p Pipeline, cache *Cache) ([]Job, bool, error) {
	completed := isCompleted(p.Status)

	if completed && cache != nil {
		if jobs, ok := cache.get(p.ID); ok {
			return jobs, true, nil
		}
	}

	jobs, err := c.fetchJobsFromAPI(p.ID)
	if err != nil {
		return nil, false, err
	}

	if completed && cache != nil {
		_ = cache.set(p.ID, jobs) // best-effort
	}

	return jobs, false, nil
}

// fetchJobsFromAPI fetches all job pages for a single pipeline.
func (c *gitLabClient) fetchJobsFromAPI(pipelineID int) ([]Job, error) {
	var all []Job
	page := 1

	for {
		endpoint := fmt.Sprintf(
			"projects/%s/pipelines/%d/jobs?per_page=100&page=%d",
			c.projectEncoded, pipelineID, page,
		)

		data, err := glabAPI(endpoint)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", page, err)
		}

		var batch []Job
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, fmt.Errorf("page %d: parse error: %w", page, err)
		}

		if len(batch) == 0 {
			break
		}

		for i := range batch {
			batch[i].PipelineID = pipelineID
		}

		all = append(all, batch...)
		page++
	}

	return all, nil
}

// glabAPI runs `glab api <endpoint>` and returns the raw JSON bytes.
func glabAPI(endpoint string) ([]byte, error) {
	out, err := exec.Command("glab", "api", endpoint).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		return nil, fmt.Errorf("%s", msg)
	}
	return out, nil
}

// isCompleted returns true for terminal pipeline statuses that won't change.
func isCompleted(status string) bool {
	switch status {
	case "success", "failed", "canceled", "skipped":
		return true
	}
	return false
}
