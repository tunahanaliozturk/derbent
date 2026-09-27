# Contributing to Derbent

Thank you for helping. This page covers how to build and test Derbent, how design decisions are
recorded, and what every commit needs.

## Build and test

You need Go 1.27.1 and nothing else: no C compiler, since SQLite comes from `modernc.org/sqlite`, which
is pure Go ([ADR 0008](docs/adr/0008-sqlite-without-cgo.md)). `go.mod` names go1.27.1 as its toolchain,
so an older `go` command, from Go 1.21 on, fetches it by itself unless `GOTOOLCHAIN` says otherwise.

```bash
go build ./cmd/derbent
```

Four gates must pass before every commit. Run each one and check that it succeeded:

```bash
go vet ./...
go test ./... -count=1
go run mvdan.cc/gofumpt@v0.12.0 -l .
go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.14.0 run ./...
```

gofumpt must print nothing, and golangci-lint must report 0 issues. Some tests start the test binary as
the `derbent` command or as a small MCP server, and one starts four processes that append receipts at
once; `go test -short ./...` skips that one.

On every push and pull request, CI also checks that `go mod tidy` changes nothing, runs the tests with
`-race` on Linux and Windows, checks the licence of every module in the build against the allow list
(Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, ISC), builds on macOS, and runs `scripts/release.sh` to show
that two builds of every release binary are identical. The race detector needs cgo, so run `-race`
locally only if you have a C compiler.

A few rules the code follows:

- Functions that do I/O take a `context.Context` as their first argument.
- Errors are wrapped with `%w` and start in lower case.
- The standard output of `derbent mcp` carries MCP frames and nothing else; anything for people goes to
  standard error.
- Every package that starts goroutines checks for leaks with goleak.
- Text files use LF line endings (`.gitattributes` enforces it).
- A change to behaviour comes with a test that fails without it.

## Plans and decisions

[docs/design.md](docs/design.md) is the contract: what Derbent does, what it does not do, and how each
claim is tested. A change bigger than a fix starts with a plan, in the issue or the pull request: what
changes, which files it touches, and how it will be tested. If it changes what the design says, the pull
request updates the design too.

A decision someone could reasonably make differently gets an ADR in [docs/adr](docs/adr): a short file
numbered after the last one, with its context, the decision and its consequences, and a row in the
design's Decisions table. When a decision changes, a new ADR says what changed and why, and the old one's
status names the ADR that replaced it.

Documentation is plain English written for a user. Every claim in the README names the test or the
measurement behind it; a claim with nothing behind it is left out.

## Sign your commits

Every commit in a pull request carries a `Signed-off-by` line with your name and email, certifying the
[Developer Certificate of Origin](https://developercertificate.org/): that you wrote the change, or
otherwise have the right to submit it under the project's licence. `git commit -s` adds the line for you.
To sign commits you already made on your branch, run `git rebase --signoff main` and push again.
