package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// renderJSON writes the report as formatted JSON to stdout.
func renderJSON(r Report) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
}

// renderTerminal prints a human-readable report to stdout.
func renderTerminal(r Report) {
	sep := "────────────────────────────────────────"

	fmt.Println()
	fmt.Println("  GitLab Pipeline Reliability Report")
	fmt.Printf("  Project : %s\n", r.Project)
	fmt.Printf("  Period  : %s  (since %s)\n", r.Period, r.Since.Format("2006-01-02T15:04:05Z"))
	fmt.Println(" ", sep)
	fmt.Println()

	fmt.Println("  PIPELINES")
	printRow("Total", fmt.Sprintf("%d", r.Pipelines.Total))
	printRow("Success", fmt.Sprintf("%d", r.Pipelines.Success))
	printRow("Failed", fmt.Sprintf("%d", r.Pipelines.Failed))
	if r.Pipelines.Other > 0 {
		printRow("Other (skipped…)", fmt.Sprintf("%d", r.Pipelines.Other))
	}
	printRow("Success rate", r.Pipelines.SuccessRate)
	fmt.Println()

	fmt.Println(" ", sep)
	fmt.Println()

	fmt.Println("  JOBS")
	printRow("Total", fmt.Sprintf("%d", r.Jobs.Total))
	printRow("Success", fmt.Sprintf("%d", r.Jobs.Success))
	printRow("Failed", fmt.Sprintf("%d", r.Jobs.Failed))
	printRow("Success rate", r.Jobs.SuccessRate)
	fmt.Println()

	fmt.Println("  JOB FAILURE BREAKDOWN")
	printRow("Code failures", fmt.Sprintf("%d", r.Jobs.FailureBreakdown.Code))
	printRow("Infra failures", fmt.Sprintf("%d", r.Jobs.FailureBreakdown.Infra))

	if r.Jobs.FailureBreakdown.UnknownFailureCount > 0 {
		fmt.Println()
		fmt.Printf("  * %d job(s) failed with 'unknown_failure' — classified as infra\n",
			r.Jobs.FailureBreakdown.UnknownFailureCount)
		fmt.Println("    but the root cause is ambiguous. Investigate runner logs for those jobs.")
	}

	if len(r.Jobs.TopFailingJobs) > 0 {
		fmt.Println()
		fmt.Println(" ", sep)
		fmt.Println()
		fmt.Println("  TOP FAILING JOBS")
		fmt.Printf("  %-40s %-10s %s\n", "Job name", "Type", "Failures")
		fmt.Printf("  %-40s %-10s %s\n", "--------", "----", "--------")
		for _, j := range r.Jobs.TopFailingJobs {
			fmt.Printf("  %-40s %-10s %d\n", j.Name, j.FailureType, j.Failures)
		}
	}

	fmt.Println()
	fmt.Println(" ", sep)
	fmt.Println()
}

func printRow(label, value string) {
	fmt.Printf("  %-22s %s\n", label, value)
}
