package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const fetchWorkers = 8

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

// fetchAllJobs fetches jobs for every pipeline in parallel, using the cache for completed ones.
func (c *gitLabClient) fetchAllJobs(pipelines []Pipeline, cache *Cache, quiet bool) ([]Job, error) {
	total := len(pipelines)

	type result struct {
		index  int
		jobs   []Job
		cached bool
		err    error
	}

	work := make(chan int, total)
	results := make(chan result, total)

	var done atomic.Int64
	var totalJobs atomic.Int64

	// Progress printer — runs in its own goroutine, updates every 100ms.
	stopProgress := make(chan struct{})
	if !quiet {
		go func() {
			for {
				select {
				case <-stopProgress:
					return
				default:
					fmt.Fprintf(os.Stderr, "\r  Fetching jobs… %d of %d pipelines done, %d jobs so far   ",
						done.Load(), total, totalJobs.Load())
					time.Sleep(100 * time.Millisecond)
				}
			}
		}()
	}

	// Enqueue all pipeline indices.
	for i := range pipelines {
		work <- i
	}
	close(work)

	// Launch worker pool.
	var wg sync.WaitGroup
	workers := fetchWorkers
	if workers > total {
		workers = total
	}
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				jobs, cached, err := c.jobsForPipeline(pipelines[i], cache)
				results <- result{index: i, jobs: jobs, cached: cached, err: err}
				if err == nil {
					done.Add(1)
					totalJobs.Add(int64(len(jobs)))
				}
			}
		}()
	}

	// Close results once all workers finish.
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results in original order.
	ordered := make([][]Job, total)
	var firstErr error
	for r := range results {
		if r.err != nil && firstErr == nil {
			firstErr = fmt.Errorf("pipeline %d: %w", pipelines[r.index].ID, r.err)
		}
		ordered[r.index] = r.jobs
	}

	if !quiet {
		close(stopProgress)
		fmt.Fprintf(os.Stderr, "\r  Fetching jobs… %d of %d pipelines done, %d jobs so far   \n",
			done.Load(), total, totalJobs.Load())
	}

	if firstErr != nil {
		return nil, firstErr
	}

	var all []Job
	for _, jobs := range ordered {
		all = append(all, jobs...)
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
