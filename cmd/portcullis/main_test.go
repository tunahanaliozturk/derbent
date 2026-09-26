package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out bytes.Buffer
	if err := run(t.Context(), []string{"version"}, strings.NewReader(""), &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(out.String()); got != "dev" {
		t.Fatalf("version = %q, want dev", got)
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	err := run(t.Context(), []string{"nope"}, strings.NewReader(""), io.Discard, io.Discard)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
}

func TestRunWithoutArgumentsShowsUsage(t *testing.T) {
	err := run(t.Context(), nil, strings.NewReader(""), io.Discard, io.Discard)
	if !errors.Is(err, errUsage) {
		t.Fatalf("err = %v, want errUsage", err)
	}
}
