package redact_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tunahanaliozturk/derbent/internal/redact"
)

func mustNew(t *testing.T, patterns, secrets []string) *redact.Redactor {
	t.Helper()
	r, err := redact.New(patterns, secrets)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestJSONMasksStringValuesAndStaysValid(t *testing.T) {
	r := mustNew(t, []string{`(?i)bearer\s+\S+`, `ghp_[A-Za-z0-9]{36}`}, nil)
	token := "ghp_" + strings.Repeat("a", 36)
	in := `{"header":"Bearer abc.def","nested":{"list":["x","` + token + `"]},"n":12345678901234567890,"ok":true}`
	out := r.JSON(in)
	if strings.Contains(out, "abc.def") || strings.Contains(out, token) {
		t.Fatalf("secret left in %s", out)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if !strings.Contains(out, "12345678901234567890") || !strings.Contains(out, `"ok":true`) {
		t.Fatalf("non-string values changed: %s", out)
	}
}

func TestJSONMasksObjectKeys(t *testing.T) {
	r := mustNew(t, nil, []string{"s3cret-header-value"})
	if out := r.JSON(`{"headers":{"s3cret-header-value":"x"}}`); out != `{"headers":{"[redacted]":"x"}}` {
		t.Fatalf("JSON = %s", out)
	}
}

func TestSecretsAreMaskedLongestFirst(t *testing.T) {
	r := mustNew(t, nil, []string{"short-secret", "short-secret-and-more", "tiny"})
	out := r.String("a short-secret-and-more b tiny")
	if out != "a [redacted] b tiny" {
		t.Fatalf("String = %q; values under 8 characters are not masked, longer ones are masked whole", out)
	}
}

func TestNoPatternsLeavesDocumentAlone(t *testing.T) {
	in := `{"b":1,"a":"<x>"}`
	if out := mustNew(t, nil, nil).JSON(in); out != in {
		t.Fatalf("JSON = %s, want the input untouched", out)
	}
	var nilRedactor *redact.Redactor
	if out := nilRedactor.JSON(in); out != in {
		t.Fatalf("nil redactor changed the input: %s", out)
	}
}

func TestInvalidJSONIsMaskedAsText(t *testing.T) {
	r := mustNew(t, []string{`sk-[a-z]+`}, nil)
	if out := r.JSON(`not json sk-abc`); out != "not json [redacted]" {
		t.Fatalf("JSON = %q", out)
	}
}

func TestBadPatternIsAnError(t *testing.T) {
	if _, err := redact.New([]string{"("}, nil); err == nil || !strings.Contains(err.Error(), "redact pattern 1") {
		t.Fatalf("err = %v", err)
	}
}
