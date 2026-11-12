# gha-stranded

**Find pull requests blocked by required GitHub Actions checks that never report.**

A PR can show **“Expected — Waiting for status to be reported”** while its
workflow sits in `queued` with no jobs. There may be no completed run to rerun
and no useful button on the PR. Checking each workflow by hand gets tedious
when several PRs are affected.

`gha-stranded` identifies PRs with this pattern and prints their links alongside
`gh run cancel` and `gh run rerun` commands to try. It only reads GitHub data;
you decide whether to run the suggested commands.

**Current scope:** this is an unofficial diagnostic tool for
[stackrox/stackrox](https://github.com/stackrox/stackrox). The repository is fixed
in the code; there is no `--repo` option yet. It is independently maintained and
is not an official GitHub or StackRox service.

## Quick start

You need Go 1.26 or newer to build and an authenticated GitHub CLI (`gh`) to run.
The source uses only the Go standard library. The compiled binary needs `gh`,
but does not need a Go installation.

From your checkout of this repository:

```sh
gh auth status --hostname github.com
go build -o gha-stranded ./cmd/gha-stranded
./gha-stranded --pr 23935
```

If you have not authenticated `gh`, run `gh auth login --hostname github.com`
first. Your GitHub account must be able to read the repository's PRs, Actions
runs, and applicable rules. The detector issues only GET requests and delegates
credential handling to `gh`.

PR #23935 was a real example during development. Its status can change; use the
number of the PR you want to inspect.

## Examples

### Inspect one PR

```sh
./gha-stranded --pr 23935
```

Uses that PR's actual base branch and checks its current head commit. This is
the quickest way to investigate one suspected case. The PR must be open.

### Scan PRs targeting a branch

```sh
./gha-stranded master
./gha-stranded release-5.0
```

The positional argument is the **base branch**, not the PR's source branch.
For example, a scan of `release-5.0` excludes PRs targeting `master`.

### Scan the default branches

```sh
./gha-stranded
```

Scans open PRs targeting `master` and the three highest `release-X.Y` branches,
sorted by numeric version. Branches are discovered on each invocation. For
example, if the latest versions are 5.0, 4.11, and 4.10, those are selected;
`release-4.9` sorts below `release-4.10`.

A full scan can take several minutes. It makes API requests for the selected
PRs and resolves workflow job names from history. Progress goes to stderr;
the report is emitted after scanning and confirming candidates.

### Adjust the detection threshold

```sh
./gha-stranded --min-age 1h --confirm-delay 15s --pr 23935
./gha-stranded --min-age 4h master
```

| Argument | Default | Meaning |
| --- | --- | --- |
| `--pr NUMBER` | Unset | Inspect one open PR instead of scanning base branches. |
| `--min-age DURATION` | `2h` | Minimum age of a queued workflow run. |
| `--confirm-delay DURATION` | `10s` | Minimum interval between candidate detection and confirmation. |
| `BASE_BRANCH` | Automatic selection | Inspect open PRs targeting this branch. |

Durations must be positive. Put flags before the optional base branch.
`--pr` cannot be combined with a base branch.

### Save the report

```sh
./gha-stranded master > stranded-prs.md
```

The report is Markdown. Progress and warnings remain visible in the terminal.
To save both streams separately:

```sh
./gha-stranded master > stranded-prs.md 2> scan.log
```

## What the report means

An excerpt from the November 12, 2026 investigation of PR #23935:

````text
1. https://github.com/stackrox/stackrox/pull/23935

   ```sh
   # Cancel first; rerun after cancellation completes.
   gh run cancel 43098560612 --repo stackrox/stackrox
   gh run rerun 43098560612 --repo stackrox/stackrox
   ```
````

A PR can have several affected workflows, each with its own command pair. The
report identifies **likely stranded workflows**, not a proven GitHub outage or
a guarantee that restarting will work.

For a current finding, try cancellation first. Wait for the run to reach a
completed state before attempting the rerun. GitHub can reject these commands
when its run state is inconsistent; the detector does not attempt recovery or
prove that a run is restartable.

`No matching PRs found` means that no PR met this specific detection rule in the
selected scope. It does not mean that all CI is healthy.

## How detection works

A PR is reported when all of these conditions hold:

1. A required GitHub Actions check has no matching result for the current PR
   revision. Requirements come from applicable rulesets and legacy branch
   protection, rather than only from checks that have already reported.
2. Historical job names connect that missing check to a PR workflow. This
   includes expanded matrix names such as `push-main-manifests (RHACS_BRANDING)`.
3. The workflow run remains `queued`, has zero jobs, and is at least the minimum
   age. No completed PR run of the same workflow exists for that head revision.
4. A second observation confirms the condition. The PR must still be open,
   with the same head commit and base branch.

The detector examines checks on both the head and test merge commits. Manual
workflow dispatches do not substitute for PR-event checks in this detector.
Queued runs from older PR revisions are excluded.

GitHub sometimes returns an empty PR association for a fork's workflow run.
In that case, the detector matches the source repository, branch, and commit
to a unique open PR across all base branches. It skips ambiguous matches.

## Warnings and limits

A warning such as:

```text
Warning: PR #23935: no historical job mapping for Scanner NVD update; skipped
```

means the detector could not establish which checks that workflow produces.
It searches up to 100 recent completed PR runs per workflow for a usable run
on the same base branch. Missing history or changed job names can prevent a
match. The workflow is left unclassified rather than assumed healthy.

These warnings do not invalidate a reported PR. The PR is included because
other workflows satisfied the detection rule. Warnings can repeat during the
confirmation pass, and skipped mappings can make the report incomplete.

This tool does not diagnose failed tests, running jobs that have hung, workflows
that never received a run, or CI from other providers such as Prow and Konflux.
Its focus is missing required Actions checks backed by old, empty queued runs.

GitHub's APIs do not provide an atomic snapshot. PR edits, rule changes, and
workflow progress can make results stale. API failures stop the scan with a
nonzero exit code; a legacy-protection 404 is treated as no legacy protection.
Use credentials with sufficient repository access. There is no persistent
cache or background service.

## Development

```sh
go test ./...
go vet ./...
go build -o gha-stranded ./cmd/gha-stranded
```

The Makefile also provides `make test`, `make vet`, and `make build`.
If you have golangci-lint v2 installed, run `golangci-lint run ./...` to use the
included configuration. It is optional for building and running the tool.

Tests use API fixtures and require no GitHub credentials. They cover pagination,
required-check providers, fork associations, ambiguous matches, single-PR
selection, and changes between detection and confirmation.

| Exit code | Meaning |
| --- | --- |
| `0` | Scan completed, with or without matches; also used for `--help`. |
| `1` | Runtime or API error. |
| `2` | Invalid CLI arguments. |
| `130` | Scan interrupted through its cancellation context. |
