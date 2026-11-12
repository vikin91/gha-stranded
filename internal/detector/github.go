package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

const repository = "stackrox/stackrox"

var errNotFound = errors.New("GitHub resource not found")

type (
	// getJSON permits API fixtures without subprocesses or network access in tests.
	getJSON    func(context.Context, string, any) error
	sourceRepo struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	ref struct {
		Ref  string     `json:"ref"`
		SHA  string     `json:"sha"`
		Repo sourceRepo `json:"repo"`
	}
	pullRequest struct {
		Number   int    `json:"number"`
		URL      string `json:"html_url"`
		State    string `json:"state"`
		Head     ref    `json:"head"`
		Base     ref    `json:"base"`
		MergeSHA string `json:"merge_commit_sha"`
	}
	workflowRun struct {
		HeadSHA      string        `json:"head_sha"`
		HeadBranch   string        `json:"head_branch"`
		HeadRepo     sourceRepo    `json:"head_repository"`
		ID           int64         `json:"id"`
		WorkflowID   int64         `json:"workflow_id"`
		SuiteID      int64         `json:"check_suite_id"`
		Name         string        `json:"name"`
		Status       string        `json:"status"`
		Conclusion   string        `json:"conclusion"`
		Created      time.Time     `json:"created_at"`
		PullRequests []pullRequest `json:"pull_requests"`
	}
	checkRun struct {
		Name string `json:"name"`
		App  struct {
			ID int64 `json:"id"`
		} `json:"app"`
		Suite struct {
			ID int64 `json:"id"`
		} `json:"check_suite"`
	}
	job struct {
		Name string `json:"name"`
	}
	requirement struct {
		Context       string `json:"context"`
		IntegrationID *int64 `json:"integration_id"`
		AppID         *int64 `json:"app_id"`
	}
)

// ghGet only issues GET requests and leaves credential management to gh.
func ghGet(ctx context.Context, path string, dst any) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "api", "--hostname", "github.com", "--method", "GET", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("GET %s: %w", path, ctx.Err())
		}
		if strings.Contains(stderr.String(), "(HTTP 404)") {
			return fmt.Errorf("GET %s: %w", path, errNotFound)
		}
		return fmt.Errorf("GET %s: %w: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode GET %s: %w", path, err)
	}
	return nil
}

// page reads array endpoints and collection envelopes without truncating results.
func page[T any](ctx context.Context, get getJSON, path, field string) ([]T, error) {
	var all []T
	for n := 1; ; n++ {
		items, err := fetchPage[T](ctx, get, path, field, n, 100)
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < 100 {
			return all, nil
		}
	}
}

func fetchPage[T any](ctx context.Context, get getJSON, path, field string, n, size int) ([]T, error) {
	u, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	query := u.Query()
	query.Set("page", strconv.Itoa(n))
	query.Set("per_page", strconv.Itoa(size))
	u.RawQuery = query.Encode()
	var raw json.RawMessage
	if err := get(ctx, u.String(), &raw); err != nil {
		return nil, err
	}
	if field != "" {
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return nil, err
		}
		var ok bool
		raw, ok = envelope[field]
		if !ok {
			return nil, fmt.Errorf("GET %s: missing %s collection", path, field)
		}
	}
	var items []T
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("decode GET %s: %w", path, err)
	}
	return items, nil
}

func repoPath(path string) string { return "repos/" + repository + "/" + path }

func requiredChecks(ctx context.Context, get getJSON, base string, appID int64) (map[string]bool, error) {
	type rule struct {
		Type       string `json:"type"`
		Parameters struct {
			Checks []requirement `json:"required_status_checks"`
		} `json:"parameters"`
	}
	rules, err := page[rule](ctx, get, repoPath("rules/branches/"+url.PathEscape(base)), "")
	if err != nil {
		return nil, err
	}
	names := make(map[string]bool)
	eligible := func(id *int64) bool { return id == nil || *id == -1 || *id == appID }
	for _, rule := range rules {
		if rule.Type != "required_status_checks" {
			continue
		}
		for _, check := range rule.Parameters.Checks {
			if eligible(check.IntegrationID) {
				names[check.Context] = true
			}
		}
	}
	var protection struct {
		Checks   []requirement `json:"checks"`
		Contexts []string      `json:"contexts"`
	}
	err = get(ctx, repoPath("branches/"+url.PathEscape(base)+"/protection/required_status_checks"), &protection)
	if errors.Is(err, errNotFound) {
		return names, nil
	}
	if err != nil {
		return nil, err
	}
	pinned := make(map[string]bool)
	for _, check := range protection.Checks {
		pinned[check.Context] = true
		if eligible(check.AppID) {
			names[check.Context] = true
		}
	}
	for _, name := range protection.Contexts {
		if !pinned[name] {
			names[name] = true
		}
	}
	return names, nil
}
