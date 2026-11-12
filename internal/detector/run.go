package detector

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

type (
	// Options selects PR bases or a single PR and the minimum queue age before confirmation.
	Options struct {
		Branch       string
		PR           int
		MinAge       time.Duration
		ConfirmDelay time.Duration
	}
	pending struct {
		pr       pullRequest
		required map[string]bool
		ids      map[int64]bool
		observed time.Time
	}
)

// Run prints likely stranded PRs and suggested commands without modifying GitHub.
// It uses an authenticated gh installation to issue read-only API requests.
func Run(ctx context.Context, options Options, output, diagnostics io.Writer) error {
	return scan(ctx, ghGet, options, output, diagnostics)
}

func scan(ctx context.Context, get getJSON, options Options, output, diagnostics io.Writer) error {
	if options.MinAge <= 0 || options.ConfirmDelay <= 0 {
		return fmt.Errorf("minimum age and confirmation delay must be positive")
	}
	if options.PR < 0 || (options.PR > 0 && options.Branch != "") {
		return fmt.Errorf("select either a positive PR number or a base branch")
	}
	var app struct {
		ID int64 `json:"id"`
	}
	if err := get(ctx, "apps/github-actions", &app); err != nil {
		return err
	}
	branches := []string{options.Branch}
	var selected *pullRequest
	if options.PR > 0 {
		selected = new(pullRequest)
		if err := get(ctx, repoPath(fmt.Sprintf("pulls/%d", options.PR)), selected); err != nil {
			return err
		}
		if selected.State != "open" {
			return fmt.Errorf("PR #%d is %s, not open", options.PR, selected.State)
		}
		branches = []string{selected.Base.Ref}
	} else if options.Branch == "" {
		type branch struct {
			Name string `json:"name"`
		}
		all, err := page[branch](ctx, get, repoPath("branches"), "")
		if err != nil {
			return err
		}
		names := make([]string, 0, len(all))
		for _, branch := range all {
			names = append(names, branch.Name)
		}
		branches = defaultBranches(names)
	}
	fmt.Fprintln(diagnostics, "Scanning bases: "+strings.Join(branches, ", "))
	d := detector{get: get, appID: app.ID, minAge: options.MinAge, history: make(map[historyKey]map[string]bool), diagnostics: diagnostics}
	var pendingPRs []pending
	for _, base := range branches {
		required, err := requiredChecks(ctx, get, base, app.ID)
		if err != nil {
			return err
		}
		if len(required) == 0 {
			continue
		}
		var prs []pullRequest
		if selected != nil {
			prs = []pullRequest{*selected}
		} else {
			prs, err = page[pullRequest](ctx, get, repoPath("pulls?state=open&base="+url.QueryEscape(base)), "")
			if err != nil {
				return err
			}
		}
		for _, pr := range prs {
			fmt.Fprintf(diagnostics, "Checking PR #%d (%s)\n", pr.Number, base)
			findings, err := d.find(ctx, pr, required)
			if err != nil {
				return err
			}
			if len(findings) == 0 {
				continue
			}
			ids := make(map[int64]bool)
			for _, run := range findings {
				ids[run.ID] = true
			}
			pendingPRs = append(pendingPRs, pending{pr, required, ids, time.Now()})
		}
	}
	count := 0
	for _, candidate := range pendingPRs {
		if err := wait(ctx, time.Until(candidate.observed.Add(options.ConfirmDelay))); err != nil {
			return err
		}
		var current pullRequest
		if err := get(ctx, repoPath(fmt.Sprintf("pulls/%d", candidate.pr.Number)), &current); err != nil {
			return err
		}
		if current.State != "open" || current.Head.SHA != candidate.pr.Head.SHA || current.Base.Ref != candidate.pr.Base.Ref {
			continue
		}
		findings, err := d.find(ctx, current, candidate.required)
		if err != nil {
			return err
		}
		var confirmed []workflowRun
		for _, run := range findings {
			if candidate.ids[run.ID] {
				confirmed = append(confirmed, run)
			}
		}
		if len(confirmed) == 0 {
			continue
		}
		count++
		var report strings.Builder
		fmt.Fprintf(&report, "%d. %s\n\n   ```sh\n", count, current.URL)
		for _, run := range confirmed {
			fmt.Fprintf(&report, "   # Cancel first; rerun after cancellation completes.\n   gh run cancel %d --repo %s\n   gh run rerun %d --repo %s\n", run.ID, repository, run.ID, repository)
		}
		report.WriteString("   ```\n\n")
		if _, err := io.WriteString(output, report.String()); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}
	if count == 0 {
		fmt.Fprintln(diagnostics, "No matching PRs found.")
	}
	return nil
}

func wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(max(0, duration))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
