// Package redact masks secrets in call arguments before they are stored in a receipt.
package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Mask replaces every secret found.
const Mask = "[redacted]"

// minSecret is the shortest literal secret that is masked. Shorter values, such as a port number or
// a flag, would mask ordinary text everywhere.
const minSecret = 8

// Redactor masks configured patterns and known secret values.
type Redactor struct {
	patterns []*regexp.Regexp
	secrets  []string
}

// New compiles patterns and keeps the secrets of at least eight characters, longest first so that a
// secret containing another one is masked whole.
func New(patterns, secrets []string) (*Redactor, error) {
	r := &Redactor{}
	for i, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("redact pattern %d: %w", i+1, err)
		}
		r.patterns = append(r.patterns, re)
	}
	for _, s := range secrets {
		if len(s) >= minSecret && !slices.Contains(r.secrets, s) {
			r.secrets = append(r.secrets, s)
		}
	}
	slices.SortFunc(r.secrets, func(a, b string) int { return len(b) - len(a) })
	return r, nil
}

func (r *Redactor) empty() bool {
	return r == nil || (len(r.patterns) == 0 && len(r.secrets) == 0)
}

// String masks every known secret and every pattern match in s.
func (r *Redactor) String(s string) string {
	if r.empty() {
		return s
	}
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, Mask)
	}
	for _, re := range r.patterns {
		s = re.ReplaceAllLiteralString(s, Mask)
	}
	return s
}

// JSON masks secrets inside every string value of a JSON document and returns it compact, with object
// keys sorted and numbers as written. Keys and other values are kept. Masking values one by one keeps
// the result valid JSON whatever the patterns match. Text that is not JSON is masked as plain text.
func (r *Redactor) JSON(doc string) string {
	if r.empty() {
		return doc
	}
	dec := json.NewDecoder(strings.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return r.String(doc)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r.walk(v)); err != nil {
		return r.String(doc)
	}
	return strings.TrimSuffix(buf.String(), "\n")
}

func (r *Redactor) walk(v any) any {
	switch t := v.(type) {
	case string:
		return r.String(t)
	case []any:
		for i := range t {
			t[i] = r.walk(t[i])
		}
		return t
	case map[string]any:
		for k, x := range t {
			t[k] = r.walk(x)
		}
		return t
	default:
		return v
	}
}
