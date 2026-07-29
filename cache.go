package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Cache stores job lists keyed by pipeline ID under ~/.cache/gitlab-job-stats/<project-slug>/.
// Only completed pipelines are cached (their jobs never change).
type Cache struct {
	dir string
}

func newCache(project string) (*Cache, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return nil, fmt.Errorf("could not find user cache directory: %w", err)
	}

	// Sanitise project path into a safe directory name: "group/project" → "group_project"
	slug := strings.ReplaceAll(project, "/", "_")
	dir := filepath.Join(base, "gitlab-job-stats", slug)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("could not create cache directory %s: %w", dir, err)
	}

	return &Cache{dir: dir}, nil
}

// get returns cached jobs for a pipeline ID, and whether the entry existed.
func (c *Cache) get(pipelineID int) ([]Job, bool) {
	path := c.path(pipelineID)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		// Corrupted cache entry — remove it silently.
		_ = os.Remove(path)
		return nil, false
	}

	return jobs, true
}

// set writes jobs for a pipeline ID to the cache.
func (c *Cache) set(pipelineID int, jobs []Job) error {
	data, err := json.Marshal(jobs)
	if err != nil {
		return err
	}
	return os.WriteFile(c.path(pipelineID), data, 0o644)
}

func (c *Cache) path(pipelineID int) string {
	return filepath.Join(c.dir, fmt.Sprintf("%d.json", pipelineID))
}
