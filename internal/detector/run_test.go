package detector

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestScan(t *testing.T) {
	tests := map[string]struct {
		change func(*fixture)
		want   bool
	}{
		"should report confirmed run":                             {func(*fixture) {}, true},
		"should report confirmed fork run without PR association": {func(f *fixture) { f.runs[0].PullRequests = nil }, true},
		"should suppress changed head":                            {func(f *fixture) { f.current.Head.SHA = "new" }, false},
		"should suppress changed base":                            {func(f *fixture) { f.current.Base.Ref = "master" }, false},
		"should suppress closed PR":                               {func(f *fixture) { f.current.State = "closed" }, false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newFixture()
				tt.change(f)
				var out, diagnostics bytes.Buffer
				err := scan(t.Context(), f.get, Options{MinAge: 2 * time.Hour, ConfirmDelay: 10 * time.Second}, &out, &diagnostics)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(out.String(), "gh run cancel 1 --repo stackrox/stackrox") != tt.want {
					t.Fatalf("unexpected report: %s", &out)
				}
				if tt.want && (!strings.Contains(out.String(), testPR().URL) || !strings.Contains(out.String(), "gh run rerun 1 --repo stackrox/stackrox")) {
					t.Fatalf("incomplete report: %s", &out)
				}
				if !strings.Contains(diagnostics.String(), "master, release-5.0, release-4.11, release-4.10") {
					t.Fatalf("wrong default branches: %s", &diagnostics)
				}
			})
		})
	}
}

func TestScanRecoveryDuringConfirmation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture()
		get := func(ctx context.Context, path string, dst any) error {
			if path == repoPath("pulls/23880") {
				f.jobs = 1
			}
			return f.get(ctx, path, dst)
		}
		var out bytes.Buffer
		err := scan(t.Context(), get, Options{Branch: "release-5.0", MinAge: 2 * time.Hour, ConfirmDelay: 10 * time.Second}, &out, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 {
			t.Fatalf("reported recovered run: %s", &out)
		}
	})
}

func TestWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := wait(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestScanSinglePR(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newFixture()
		f.runs[0].PullRequests = nil
		var out bytes.Buffer
		err := scan(t.Context(), f.get, Options{PR: 23880, MinAge: 2 * time.Hour, ConfirmDelay: time.Second}, &out, io.Discard)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), testPR().URL) || !strings.Contains(out.String(), "gh run cancel 1") {
			t.Fatalf("missing single-PR report: %s", &out)
		}
		for _, path := range f.calls {
			if strings.Contains(path, "pulls?base=") || strings.Contains(path, "branches?page=") {
				t.Fatalf("single-PR mode enumerated unrelated PRs or branches: %s", path)
			}
		}
	})
}

func TestScanSingleClosedPR(t *testing.T) {
	f := newFixture()
	f.current.State = "closed"
	err := scan(t.Context(), f.get, Options{PR: 23880, MinAge: time.Hour, ConfirmDelay: time.Second}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("got %v; want closed PR error", err)
	}
}
