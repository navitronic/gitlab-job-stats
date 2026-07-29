package main

import (
	"fmt"
	"sort"
	"time"
)

// FailureType classifies a job failure.
type FailureType string

const (
	FailureCode  FailureType = "code"
	FailureInfra FailureType = "infra"
)

// FailingJob summarises failures for a single job name.
type FailingJob struct {
	Name        string      `json:"name"`
	Failures    int         `json:"failures"`
	FailureType FailureType `json:"type"`
}

// FailureBreakdown counts code vs infra failures.
type FailureBreakdown struct {
	Code                int `json:"code"`
	Infra               int `json:"infra"`
	UnknownFailureCount int `json:"unknown_failure_count"`
}

// PipelineStats holds pipeline-level counts.
type PipelineStats struct {
	Total       int    `json:"total"`
	Success     int    `json:"success"`
	Failed      int    `json:"failed"`
	Other       int    `json:"other"`
	SuccessRate string `json:"success_rate"`
}

// JobStats holds job-level counts.
type JobStats struct {
	Total            int              `json:"total"`
	Success          int              `json:"success"`
	Failed           int              `json:"failed"`
	SuccessRate      string           `json:"success_rate"`
	FailureBreakdown FailureBreakdown `json:"failure_breakdown"`
	TopFailingJobs   []FailingJob     `json:"top_failing_jobs"`
}

// Report is the full aggregated reliability report.
type Report struct {
	Project   string        `json:"project"`
	Period    string        `json:"period"`
	Since     time.Time     `json:"since"`
	Pipelines PipelineStats `json:"pipelines"`
	Jobs      JobStats      `json:"jobs"`
}

// classifyFailure maps a GitLab failure_reason to code or infra.
func classifyFailure(reason string) FailureType {
	switch reason {
	case "runner_system_failure",
		"stuck_or_timeout_failure",
		"runner_unsupported",
		"scheduler_failure",
		"data_integrity_failure",
		"unknown_failure":
		return FailureInfra
	default:
		return FailureCode
	}
}

// aggregate builds a Report from raw pipeline and job slices.
func aggregate(project, period string, since time.Time, pipelines []Pipeline, jobs []Job) Report {
	// Pipeline counts
	ps := PipelineStats{Total: len(pipelines)}
	for _, p := range pipelines {
		switch p.Status {
		case "success":
			ps.Success++
		case "failed":
			ps.Failed++
		default:
			ps.Other++
		}
	}
	ps.SuccessRate = pct(ps.Success, ps.Total)

	// Job counts — deduplicate retries: keep only the latest job (highest ID)
	// per (pipelineID, name) pair.
	type key struct {
		pipelineID int
		name       string
	}
	latest := make(map[key]Job)
	for _, j := range jobs {
		k := key{j.PipelineID, j.Name}
		if existing, ok := latest[k]; !ok || j.ID > existing.ID {
			latest[k] = j
		}
	}

	js := JobStats{}
	failCounts := make(map[string]struct {
		count int
		ftype FailureType
	})
	unknownCount := 0

	for _, j := range latest {
		js.Total++
		switch j.Status {
		case "success":
			js.Success++
		case "failed":
			js.Failed++
			ft := classifyFailure(j.FailureReason)
			if ft == FailureInfra {
				js.FailureBreakdown.Infra++
				if j.FailureReason == "unknown_failure" {
					unknownCount++
				}
			} else {
				js.FailureBreakdown.Code++
			}
			entry := failCounts[j.Name]
			entry.count++
			entry.ftype = ft
			failCounts[j.Name] = entry
		}
	}

	js.FailureBreakdown.UnknownFailureCount = unknownCount
	js.SuccessRate = pct(js.Success, js.Total)

	// Build sorted top-10 failing jobs list.
	var topJobs []FailingJob
	for name, entry := range failCounts {
		topJobs = append(topJobs, FailingJob{
			Name:        name,
			Failures:    entry.count,
			FailureType: entry.ftype,
		})
	}
	sort.Slice(topJobs, func(i, j int) bool {
		return topJobs[i].Failures > topJobs[j].Failures
	})
	if len(topJobs) > 10 {
		topJobs = topJobs[:10]
	}
	js.TopFailingJobs = topJobs

	return Report{
		Project:   project,
		Period:    period,
		Since:     since,
		Pipelines: ps,
		Jobs:      js,
	}
}

// pct returns "x.x%" or "N/A" when total is zero.
func pct(n, total int) string {
	if total == 0 {
		return "N/A"
	}
	return fmt.Sprintf("%.1f%%", float64(n)/float64(total)*100)
}
