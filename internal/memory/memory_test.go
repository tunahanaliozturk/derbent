package memory_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

const shop, blog = "/work/shop", "/work/blog"

func newStore(t *testing.T) *memory.Store {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return memory.NewStore(db)
}

func note(project, title, body string, tags ...string) memory.Entry {
	return memory.Entry{Project: project, Author: "claude", Session: "s1", Title: title, Body: body, Tags: tags}
}

func write(t *testing.T, s *memory.Store, e memory.Entry, supersedes int64) int64 {
	t.Helper()
	id, err := s.Write(t.Context(), e, supersedes)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func search(t *testing.T, s *memory.Store, project, query string, all bool) []memory.Hit {
	t.Helper()
	hits, err := s.Search(t.Context(), project, query, 0, all)
	if err != nil {
		t.Fatalf("search %q: %v", query, err)
	}
	return hits
}

func TestWriteThenRead(t *testing.T) {
	s := newStore(t)
	id := write(t, s, note(shop, "  Use PostgreSQL 17  ", "The ledger moves to PostgreSQL 17.", "database", "ledger"), 0)
	got, err := s.Read(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Use PostgreSQL 17" || got.Author != "claude" || got.Project != shop || got.Session != "s1" {
		t.Fatalf("Read = %+v", got)
	}
	if strings.Join(got.Tags, ",") != "database,ledger" || got.At.IsZero() {
		t.Fatalf("tags %v at %v", got.Tags, got.At)
	}
}

func TestReadMissing(t *testing.T) {
	if _, err := newStore(t).Read(t.Context(), 42); !errors.Is(err, memory.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestWriteValidates(t *testing.T) {
	tests := map[string]memory.Entry{
		"empty title":            note(shop, "   ", "b"),
		"title over 200 letters": note(shop, strings.Repeat("ş", 201), "b"),
		"empty body":             note(shop, "t", " \n "),
		"body over 16 KiB":       note(shop, "t", strings.Repeat("x", 16<<10+1)),
		"tag with a space":       note(shop, "t", "b", "two words"),
		"eleven tags":            note(shop, "t", "b", "a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"),
		"no project":             note("", "t", "b"),
	}
	s := newStore(t)
	for name, e := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Write(t.Context(), e, 0); !errors.Is(err, memory.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
	if _, err := s.Write(t.Context(), note(shop, strings.Repeat("ş", 200), "b", "ödeme"), 0); err != nil {
		t.Fatalf("200-letter title and a Turkish tag were refused: %v", err)
	}
}

func TestSearchFindsTitleBodyAndTags(t *testing.T) {
	s := newStore(t)
	write(t, s, note(shop, "Retry policy", "Payment calls retry three times with jitter.", "payments"), 0)
	for _, q := range []string{"retry", "JITTER", "payments", "nothing jitter"} {
		if hits := search(t, s, shop, q, false); len(hits) != 1 || hits[0].Title != "Retry policy" {
			t.Errorf("search %q = %+v", q, hits)
		}
	}
}

func TestSearchNeverFailsOnSyntax(t *testing.T) {
	s := newStore(t)
	write(t, s, note(shop, "C++ build", "Use c++ 20 and NEAR the end run make."), 0)
	for _, q := range []string{`c++`, `"unbalanced`, `-minus`, `NEAR(a b)`, `run*`, `AND`, `^caret OR`, `title:x`} {
		if _, err := s.Search(t.Context(), shop, q, 0, false); err != nil {
			t.Errorf("search %q: %v", q, err)
		}
	}
	for _, q := range []string{"", "   ", `*`, `"*" -- ()`} {
		if _, err := s.Search(t.Context(), shop, q, 0, false); !errors.Is(err, memory.ErrInvalid) {
			t.Errorf("search %q: err = %v, want ErrInvalid", q, err)
		}
	}
}

func TestSearchFoldsTurkishDiacritics(t *testing.T) {
	s := newStore(t)
	write(t, s, note(shop, "Ödeme şeması kararı", "Ödemeler için şema PostgreSQL üzerinde kalır."), 0)
	for _, q := range []string{"odeme", "sema", "kararı", "üzerinde"} {
		if hits := search(t, s, shop, q, false); len(hits) != 1 {
			t.Errorf("search %q found %d", q, len(hits))
		}
	}
}

func TestSearchIsPerProjectUnlessAsked(t *testing.T) {
	s := newStore(t)
	write(t, s, note(shop, "Cache", "Redis for sessions."), 0)
	write(t, s, note(blog, "Cache", "No Redis here."), 0)
	if hits := search(t, s, shop, "redis", false); len(hits) != 1 || hits[0].Project != shop {
		t.Fatalf("project search = %+v", hits)
	}
	if hits := search(t, s, shop, "redis", true); len(hits) != 2 {
		t.Fatalf("all-project search found %d", len(hits))
	}
}

func TestSupersede(t *testing.T) {
	s := newStore(t)
	old := write(t, s, note(shop, "Queue", "Use RabbitMQ."), 0)
	newer := write(t, s, note(shop, "Queue", "Use NATS instead of RabbitMQ."), old)
	hits := search(t, s, shop, "rabbitmq", false)
	if len(hits) != 1 || hits[0].ID != newer {
		t.Fatalf("search = %+v, want only the newer note", hits)
	}
	got, err := s.Read(t.Context(), old)
	if err != nil || got.SupersededBy != newer {
		t.Fatalf("old note = %+v, err %v", got, err)
	}
	if _, err := s.Write(t.Context(), note(shop, "Queue", "Kafka."), old); !errors.Is(err, memory.ErrInvalid) {
		t.Fatalf("superseding twice: err = %v, want ErrInvalid", err)
	}
	if _, err := s.Write(t.Context(), note(blog, "Queue", "SQS."), newer); !errors.Is(err, memory.ErrInvalid) {
		t.Fatalf("superseding across projects: err = %v, want ErrInvalid", err)
	}
	if hits := search(t, s, blog, "sqs", false); len(hits) != 0 {
		t.Fatalf("a refused write left a note behind: %+v", hits)
	}
}
