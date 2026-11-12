package detector

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

var releasePattern = regexp.MustCompile(`^release-(\d+)\.(\d+)$`)

type (
	detector struct {
		get         getJSON
		appID       int64
		minAge      time.Duration
		history     map[historyKey]map[string]bool
		diagnostics io.Writer
	}
	historyKey struct {
		workflow int64
		base     string
	}
)

func defaultBranches(names []string) []string {
	type release struct {
		name         string
		major, minor int
	}
	var releases []release
	for _, name := range names {
		match := releasePattern.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		major, err := strconv.Atoi(match[1])
		if err != nil {
			continue
		}
		minor, err := strconv.Atoi(match[2])
		if err != nil {
			continue
		}
		releases = append(releases, release{name, major, minor})
	}
	slices.SortFunc(releases, func(a, b release) int {
		return cmp.Or(cmp.Compare(b.major, a.major), cmp.Compare(b.minor, a.minor))
	})
	branches := []string{"master"}
	for _, release := range releases[:min(3, len(releases))] {
		branches = append(branches, release.name)
	}
	return branches
}

func belongsToPR(run workflowRun, pr pullRequest) bool {
	return slices.ContainsFunc(run.PullRequests, func(item pullRequest) bool { return item.Number == pr.Number })
}

// associateRuns recovers omitted fork PR associations only when the source
// repository, branch and revision identify exactly one open PR across all bases.
func (d *detector) associateRuns(ctx context.Context, runs []workflowRun, pr pullRequest) error {
	var checked, unique bool
	for i := range runs {
		run := &runs[i]
		if len(run.PullRequests) != 0 || pr.Head.Repo.ID == 0 ||
			run.HeadRepo.ID != pr.Head.Repo.ID || run.HeadSHA != pr.Head.SHA ||
			pr.Head.Ref == "" || run.HeadBranch != pr.Head.Ref {
			continue
		}
		if !checked {
			owner, _, ok := strings.Cut(pr.Head.Repo.FullName, "/")
			if !ok || owner == "" {
				return fmt.Errorf("PR #%d: missing source repository name", pr.Number)
			}
			path := repoPath("pulls?state=open&head=" + url.QueryEscape(owner+":"+pr.Head.Ref))
			matches, err := page[pullRequest](ctx, d.get, path, "")
			if err != nil {
				return err
			}
			var count int
			var number int
			for _, candidate := range matches {
				if candidate.State == "open" && candidate.Head.Repo.ID == pr.Head.Repo.ID &&
					candidate.Head.Ref == pr.Head.Ref && candidate.Head.SHA == pr.Head.SHA {
					count++
					number = candidate.Number
				}
			}
			unique = count == 1 && number == pr.Number
			checked = true
			if !unique {
				fmt.Fprintf(d.diagnostics, "Warning: PR #%d: source matches are ambiguous or missing; unassociated runs skipped\n", pr.Number)
			}
		}
		if unique {
			run.PullRequests = []pullRequest{pr}
		}
	}
	return nil
}

func (d *detector) oldQueue(run workflowRun, now time.Time) bool {
	return run.Status == "queued" && !run.Created.IsZero() && now.Sub(run.Created) >= d.minAge
}

// historicalNames maps expanded matrix names using a recent run on the same base.
// The bounded history lookup deliberately leaves unverifiable mappings unknown.
func (d *detector) historicalNames(ctx context.Context, workflow int64, base string) (map[string]bool, error) {
	key := historyKey{workflow, base}
	if names, ok := d.history[key]; ok {
		return names, nil
	}
	names := make(map[string]bool)
	path := repoPath(fmt.Sprintf("actions/workflows/%d/runs?event=pull_request&status=completed", workflow))
	for n := 1; n <= 5; n++ {
		runs, err := fetchPage[workflowRun](ctx, d.get, path, "workflow_runs", n, 20)
		if err != nil {
			return nil, err
		}
		for _, run := range runs {
			if slices.Contains([]string{"skipped", "cancelled", "startup_failure"}, run.Conclusion) {
				continue
			}
			if !slices.ContainsFunc(run.PullRequests, func(pr pullRequest) bool { return pr.Base.Ref == base }) {
				continue
			}
			jobs, err := page[job](ctx, d.get, repoPath(fmt.Sprintf("actions/runs/%d/jobs", run.ID)), "jobs")
			if err != nil {
				return nil, err
			}
			for _, job := range jobs {
				names[job.Name] = true
			}
			if len(names) > 0 {
				break
			}
		}
		if len(names) > 0 || len(runs) < 20 {
			break
		}
	}
	d.history[key] = names
	return names, nil
}

func (d *detector) find(ctx context.Context, pr pullRequest, required map[string]bool) ([]workflowRun, error) {
	path := repoPath("actions/runs?event=pull_request&head_sha=" + url.QueryEscape(pr.Head.SHA))
	queued, err := page[workflowRun](ctx, d.get, path+"&status=queued", "workflow_runs")
	if err != nil {
		return nil, err
	}
	if err := d.associateRuns(ctx, queued, pr); err != nil {
		return nil, err
	}
	now := time.Now()
	if !slices.ContainsFunc(queued, func(run workflowRun) bool { return belongsToPR(run, pr) && d.oldQueue(run, now) }) {
		return nil, nil
	}
	runs, err := page[workflowRun](ctx, d.get, path, "workflow_runs")
	if err != nil {
		return nil, err
	}
	if err := d.associateRuns(ctx, runs, pr); err != nil {
		return nil, err
	}
	completed, suites := make(map[int64]bool), make(map[int64]bool)
	for _, run := range runs {
		if !belongsToPR(run, pr) {
			continue
		}
		suites[run.SuiteID] = true
		if run.Status == "completed" {
			completed[run.WorkflowID] = true
		}
	}
	candidates := slices.DeleteFunc(runs, func(run workflowRun) bool {
		return !belongsToPR(run, pr) || completed[run.WorkflowID] || !d.oldQueue(run, now)
	})
	if len(candidates) == 0 {
		return nil, nil
	}
	reported := make(map[string]bool)
	for _, sha := range slices.Compact([]string{pr.Head.SHA, pr.MergeSHA}) {
		if sha == "" {
			continue
		}
		checks, err := page[checkRun](ctx, d.get, repoPath("commits/"+url.PathEscape(sha)+"/check-runs?filter=all"), "check_runs")
		if err != nil {
			return nil, err
		}
		for _, check := range checks {
			// A manual dispatch on the head SHA does not supply a PR-event check.
			if check.App.ID == d.appID && suites[check.Suite.ID] {
				reported[check.Name] = true
			}
		}
	}
	missing := make(map[string]bool)
	for name := range required {
		if !reported[name] {
			missing[name] = true
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	var findings []workflowRun
	for _, run := range candidates {
		var jobs struct {
			Total int `json:"total_count"`
		}
		if err := d.get(ctx, repoPath(fmt.Sprintf("actions/runs/%d/jobs?per_page=1", run.ID)), &jobs); err != nil {
			return nil, err
		}
		if jobs.Total != 0 {
			continue
		}
		names, err := d.historicalNames(ctx, run.WorkflowID, pr.Base.Ref)
		if err != nil {
			return nil, err
		}
		if len(names) == 0 {
			fmt.Fprintf(d.diagnostics, "Warning: PR #%d: no historical job mapping for %s; skipped\n", pr.Number, run.Name)
		}
		for name := range missing {
			if names[name] {
				findings = append(findings, run)
				break
			}
		}
	}
	return findings, nil
}
