package detector

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

const testCheck = "push-matching-collector-scanner (RHACS_BRANDING)"

type fixture struct {
	runs         []workflowRun
	checks       map[string][]checkRun
	jobs         int
	historyNames []job
	history      []workflowRun
	current      pullRequest
	sourcePRs    []pullRequest
	calls        []string
}

func testPR() pullRequest {
	return pullRequest{Number: 23880, URL: "https://github.com/stackrox/stackrox/pull/23880", State: "open",
		Head: ref{SHA: "current", Ref: "fix", Repo: sourceRepo{ID: 42, FullName: "fork/repo"}}, Base: ref{Ref: "release-5.0"}, MergeSHA: "merge"}
}

func testRun(id int64, status string) workflowRun {
	return workflowRun{ID: id, WorkflowID: 5, SuiteID: id * 10, Name: "Build", Status: status,
		HeadSHA: "current", HeadBranch: "fix", HeadRepo: sourceRepo{ID: 42, FullName: "fork/repo"},
		Created: time.Now().Add(-3 * time.Hour), PullRequests: []pullRequest{testPR()}}
}

func newFixture() *fixture {
	return &fixture{runs: []workflowRun{testRun(1, "queued")}, checks: make(map[string][]checkRun),
		historyNames: []job{{Name: testCheck}}, history: []workflowRun{testRun(99, "completed")}, current: testPR(), sourcePRs: []pullRequest{testPR()}}
}

func reply(dst, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dst)
}

func (f *fixture) get(ctx context.Context, path string, dst any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.calls = append(f.calls, path)
	u, err := url.Parse(path)
	if err != nil {
		return err
	}
	switch {
	case u.Path == "apps/github-actions":
		return reply(dst, map[string]int64{"id": 15368})
	case u.Path == repoPath("branches"):
		return reply(dst, []map[string]string{{"name": "master"}, {"name": "release-4.9"}, {"name": "release-4.10"}, {"name": "release-4.11"}, {"name": "release-5.0"}})
	case strings.Contains(u.Path, "/rules/branches/"):
		return reply(dst, []any{map[string]any{"type": "required_status_checks", "parameters": map[string]any{
			"required_status_checks": []any{map[string]any{"context": testCheck, "integration_id": 15368}},
		}}})
	case strings.HasSuffix(u.Path, "/protection/required_status_checks"):
		return errNotFound
	case u.Path == repoPath("pulls"):
		if u.Query().Get("head") == "fork:fix" {
			return reply(dst, f.sourcePRs)
		}
		if u.Query().Get("base") != "release-5.0" {
			return reply(dst, []pullRequest{})
		}
		return reply(dst, []pullRequest{testPR()})
	case u.Path == repoPath("pulls/23880"):
		return reply(dst, f.current)
	case u.Path == repoPath("actions/runs"):
		if u.Query().Get("head_sha") != "current" || u.Query().Get("event") != "pull_request" {
			return fmt.Errorf("run lookup must use current SHA and PR event: %s", path)
		}
		var runs []workflowRun
		for _, run := range f.runs {
			if status := u.Query().Get("status"); status != "" && run.Status != status {
				continue
			}
			runs = append(runs, run)
		}
		return reply(dst, map[string]any{"workflow_runs": runs})
	case strings.Contains(u.Path, "/actions/workflows/"):
		return reply(dst, map[string]any{"workflow_runs": f.history})
	case strings.Contains(u.Path, "/commits/"):
		sha := strings.Split(strings.Split(u.Path, "/commits/")[1], "/")[0]
		return reply(dst, map[string]any{"check_runs": f.checks[sha]})
	case strings.HasSuffix(u.Path, "/jobs"):
		if u.Query().Get("per_page") == "1" {
			return reply(dst, map[string]int{"total_count": f.jobs})
		}
		return reply(dst, map[string]any{"jobs": f.historyNames})
	default:
		return fmt.Errorf("unexpected API path: %s", path)
	}
}

func TestDefaultBranches(t *testing.T) {
	tests := map[string]struct{ names, want []string }{
		"should sort versions numerically": {
			[]string{"release-4.9", "release-4.11", "release-4.10", "release-5.0", "release-99.0-rc1", "feature"},
			[]string{"master", "release-5.0", "release-4.11", "release-4.10"},
		},
		"should allow fewer releases": {[]string{"release-5.0"}, []string{"master", "release-5.0"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := defaultBranches(tt.names); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v; want %v", got, tt.want)
			}
		})
	}
}

func TestFind(t *testing.T) {
	reported := func(suite, app int64) checkRun {
		check := checkRun{Name: testCheck}
		check.App.ID = app
		check.Suite.ID = suite
		return check
	}
	tests := map[string]struct {
		change func(*fixture)
		want   bool
	}{
		"should find fork run with missing PR association": {func(f *fixture) { f.runs[0].PullRequests = nil }, true},
		"should reject mismatched source repository":       {func(f *fixture) { f.runs[0].PullRequests = nil; f.runs[0].HeadRepo.ID = 43 }, false},
		"should reject mismatched source branch":           {func(f *fixture) { f.runs[0].PullRequests = nil; f.runs[0].HeadBranch = "other" }, false},
		"should reject mismatched source commit":           {func(f *fixture) { f.runs[0].PullRequests = nil; f.runs[0].HeadSHA = "old" }, false},
		"should reject ambiguous source across bases": {func(f *fixture) {
			f.runs[0].PullRequests = nil
			other := testPR()
			other.Number = 43
			other.Base.Ref = "master"
			f.sourcePRs = append(f.sourcePRs, other)
		}, false},
		"should suppress completed fork run without association": {func(f *fixture) { r := testRun(2, "completed"); r.PullRequests = nil; f.runs = append(f.runs, r) }, false},
		"should find old empty queue":                            {func(*fixture) {}, true},
		"should ignore young queue":                              {func(f *fixture) { f.runs[0].Created = time.Now() }, false},
		"should ignore unknown queue age":                        {func(f *fixture) { f.runs[0].Created = time.Time{} }, false},
		"should ignore running workflow":                         {func(f *fixture) { f.runs[0].Status = "in_progress" }, false},
		"should ignore queue with jobs":                          {func(f *fixture) { f.jobs = 1 }, false},
		"should ignore completed same workflow":                  {func(f *fixture) { f.runs = append(f.runs, testRun(2, "completed")) }, false},
		"should not confuse different workflow completion":       {func(f *fixture) { r := testRun(2, "completed"); r.WorkflowID = 6; f.runs = append(f.runs, r) }, true},
		"should check head commit":                               {func(f *fixture) { f.checks["current"] = []checkRun{reported(10, 15368)} }, false},
		"should check merge commit":                              {func(f *fixture) { f.checks["merge"] = []checkRun{reported(10, 15368)} }, false},
		"should exclude manual dispatch checks":                  {func(f *fixture) { f.checks["current"] = []checkRun{reported(900, 15368)} }, true},
		"should exclude another app":                             {func(f *fixture) { f.checks["current"] = []checkRun{reported(10, 123)} }, true},
		"should require matching historical job":                 {func(f *fixture) { f.historyNames = []job{{Name: "unrelated"}} }, false},
		"should skip unknown mapping":                            {func(f *fixture) { f.history = nil }, false},
		"should skip another PR":                                 {func(f *fixture) { f.runs[0].PullRequests[0].Number = 42 }, false},
		"should require history on same base":                    {func(f *fixture) { f.history[0].PullRequests[0].Base.Ref = "master" }, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			tt.change(f)
			d := detector{get: f.get, appID: 15368, minAge: 2 * time.Hour, history: make(map[historyKey]map[string]bool), diagnostics: io.Discard}
			got, err := d.find(t.Context(), testPR(), map[string]bool{testCheck: true})
			if err != nil {
				t.Fatal(err)
			}
			if (len(got) > 0) != tt.want {
				t.Fatalf("got %v; want finding %v", got, tt.want)
			}
			if len(got) > 0 && got[0].ID != 1 {
				t.Fatalf("wrong run: %v", got)
			}
		})
	}
}
