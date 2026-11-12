package detector

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestPage(t *testing.T) {
	for _, field := range []string{"", "workflow_runs"} {
		t.Run("collection="+field, func(t *testing.T) {
			get := func(_ context.Context, path string, dst any) error {
				u, err := url.Parse(path)
				if err != nil {
					return err
				}
				if u.Query().Get("base") != "release/5.0" {
					return fmt.Errorf("lost base parameter")
				}
				n, err := strconv.Atoi(u.Query().Get("page"))
				if err != nil {
					return err
				}
				var items []int
				switch n {
				case 1:
					items = make([]int, 100)
				case 2:
					items = []int{42}
				default:
					t.Fatal("unexpected page")
				}
				if field == "" {
					return reply(dst, items)
				}
				return reply(dst, map[string]any{field: items})
			}
			got, err := page[int](t.Context(), get, "pulls?base=release%2F5.0", field)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 101 || got[100] != 42 {
				t.Fatalf("unexpected pagination: %v", got)
			}
		})
	}
}

func TestRequiredChecks(t *testing.T) {
	get := func(_ context.Context, path string, dst any) error {
		if strings.Contains(path, "rules/branches/") {
			return reply(dst, []any{map[string]any{"type": "required_status_checks", "parameters": map[string]any{
				"required_status_checks": []any{
					map[string]any{"context": "gha", "integration_id": 15368},
					map[string]any{"context": "konflux", "integration_id": 296509},
				},
			}}})
		}
		return reply(dst, map[string]any{"contexts": []string{"external", "unbound"}, "checks": []any{
			map[string]any{"context": "external", "app_id": 123},
		}})
	}
	got, err := requiredChecks(t.Context(), get, "release-5.0", 15368)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got["gha"] || !got["unbound"] {
		t.Fatalf("wrong requirements: %v", got)
	}
}

func TestRequiredChecksErrors(t *testing.T) {
	denied := errors.New("HTTP 403")
	for name, apiErr := range map[string]error{"should allow absent legacy protection": errNotFound, "should propagate permission errors": denied} {
		t.Run(name, func(t *testing.T) {
			get := func(_ context.Context, path string, dst any) error {
				if strings.Contains(path, "rules/branches/") {
					return reply(dst, []any{})
				}
				return apiErr
			}
			_, err := requiredChecks(t.Context(), get, "master", 15368)
			if errors.Is(apiErr, errNotFound) {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, denied) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
