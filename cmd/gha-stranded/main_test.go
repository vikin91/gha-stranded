package main

import (
	"bytes"
	"testing"
)

func TestArguments(t *testing.T) {
	tests := map[string]struct {
		args []string
		code int
	}{
		"should show help without API calls":    {[]string{"--help"}, 0},
		"should reject extra branches":          {[]string{"master", "release-5.0"}, 2},
		"should reject negative age":            {[]string{"--min-age=-1h"}, 2},
		"should reject zero confirmation delay": {[]string{"--confirm-delay=0s"}, 2},
		"should reject invalid flags":           {[]string{"--unknown"}, 2},
		"should reject zero PR number":          {[]string{"--pr", "0"}, 2},
		"should reject negative PR number":      {[]string{"--pr", "-1"}, 2},
		"should reject nonnumeric PR number":    {[]string{"--pr", "oops"}, 2},
		"should reject PR combined with branch": {[]string{"--pr", "23935", "master"}, 2},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			if got := execute(tt.args, &out, &diagnostics); got != tt.code {
				t.Fatalf("got exit %d; want %d", got, tt.code)
			}
			if out.Len() != 0 {
				t.Fatalf("diagnostics leaked into stdout: %s", &out)
			}
		})
	}
}
