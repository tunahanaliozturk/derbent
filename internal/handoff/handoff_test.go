package handoff_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.uber.org/goleak"

	"github.com/tunahanaliozturk/derbent/internal/handoff"
	"github.com/tunahanaliozturk/derbent/internal/memory"
	"github.com/tunahanaliozturk/derbent/internal/store"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const shop, blog = "/work/shop", "/work/blog"

func newStore(t *testing.T) *handoff.Store {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return handoff.NewStore(db)
}

func task(project, from, to, title string) handoff.Handoff {
	return handoff.Handoff{
		Project: project, From: from, FromSession: from + "-session", To: to, Title: title,
		Body: "Details of " + title + ".", Tags: []string{"review"},
	}
}

func create(t *testing.T, s *handoff.Store, h handoff.Handoff) int64 {
	t.Helper()
	id, err := s.Create(t.Context(), h)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func ids(list []handoff.Handoff) []int64 {
	out := make([]int64, len(list))
	for i, h := range list {
		out[i] = h.ID
	}
	return out
}

// A handoff goes from open to taken to done, and keeps who created it and who took it, from which gate
// sessions, when, and the note it was finished with.
func TestCreateTakeFinish(t *testing.T) {
	s := newStore(t)
	id := create(t, s, task(shop, "claude", "reviewer", "  Review the retry change  "))
	got, err := s.Take(t.Context(), id, "reviewer", "reviewer-session")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Review the retry change" || got.From != "claude" || got.FromSession != "claude-session" ||
		got.Body != "Details of   Review the retry change  ." || strings.Join(got.Tags, ",") != "review" ||
		got.State != handoff.Taken || got.TakenBy != "reviewer" || got.TakenSession != "reviewer-session" ||
		got.Created.IsZero() || got.Taken.IsZero() || !got.Done.IsZero() {
		t.Fatalf("Take = %+v", got)
	}
	done, err := s.Finish(t.Context(), id, "reviewer", "Backoff checked.")
	if err != nil || done.State != handoff.Done || done.Note != "Backoff checked." || done.Done.IsZero() {
		t.Fatalf("Finish = %+v, %v", done, err)
	}
	list, err := s.List(t.Context(), handoff.Filter{})
	if err != nil || len(list) != 1 || list[0].State != handoff.Done || list[0].Note != "Backoff checked." || list[0].TakenBy != "reviewer" {
		t.Fatalf("List = %+v, %v", list, err)
	}
}

// A handoff addressed to a label no agent can be started under would wait forever, so it is refused with
// the rule for labels; every label --agent takes, and *, is accepted.
func TestCreateRefusesALabelNoAgentCanHave(t *testing.T) {
	s := newStore(t)
	for _, to := range []string{"Reviewer", "claude code", "", "-reviewer", "rev*", strings.Repeat("a", 33)} {
		if _, err := s.Create(t.Context(), task(shop, "claude", to, "t")); !errors.Is(err, handoff.ErrInvalid) || !strings.Contains(err.Error(), "agent label") {
			t.Errorf("to %q: err = %v, want the rule for labels", to, err)
		}
	}
	for _, to := range []string{"reviewer", handoff.Anyone, "codex-2", "a_b", strings.Repeat("a", 32)} {
		if _, err := s.Create(t.Context(), task(shop, "claude", to, "t")); err != nil {
			t.Errorf("to %q: %v", to, err)
		}
	}
}

// A handoff's title, body and tags have the limits a memory note has, and its note holds up to 4 KiB.
func TestCreateAndFinishCheckTheirLimits(t *testing.T) {
	s := newStore(t)
	big := task(shop, "claude", "reviewer", "t")
	big.Body = strings.Repeat("b", handoff.MaxBody+1)
	blank := task(shop, "claude", "reviewer", "t")
	blank.Body = "  \n"
	tagged := task(shop, "claude", "reviewer", "t")
	tagged.Tags = []string{"no spaces allowed"}
	crowded := task(shop, "claude", "reviewer", "t")
	crowded.Tags = make([]string, memory.MaxTags+1)
	for i := range crowded.Tags {
		crowded.Tags[i] = fmt.Sprint("tag", i)
	}
	for _, tc := range []struct {
		name string
		h    handoff.Handoff
	}{
		{"a long title", task(shop, "claude", "reviewer", strings.Repeat("t", handoff.MaxTitle+1))},
		{"an empty title", task(shop, "claude", "reviewer", "   ")},
		{"a big body", big},
		{"a blank body", blank},
		{"a bad tag", tagged},
		{"one tag too many", crowded},
		{"no author", task(shop, "", "reviewer", "t")},
	} {
		if _, err := s.Create(t.Context(), tc.h); !errors.Is(err, handoff.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", tc.name, err)
		}
	}
	crowded.Tags = crowded.Tags[:memory.MaxTags]
	create(t, s, crowded)
	id := create(t, s, task(shop, "claude", "reviewer", "t"))
	if _, err := s.Take(t.Context(), id, "reviewer", "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(t.Context(), id, "reviewer", strings.Repeat("n", handoff.MaxNote+1)); !errors.Is(err, handoff.ErrInvalid) {
		t.Fatalf("a note over 4 KiB: err = %v", err)
	}
	if _, err := s.Finish(t.Context(), id, "reviewer", strings.Repeat("n", handoff.MaxNote)); err != nil {
		t.Fatalf("a note of 4 KiB: %v", err)
	}
}

// List keeps a project's handoffs addressed to the agent or to anyone, or with Mine the ones it created,
// in the state asked for, newest first; an empty filter keeps every handoff.
func TestListFiltersByProjectAddressAndState(t *testing.T) {
	s := newStore(t)
	toReviewer := create(t, s, task(shop, "claude", "reviewer", "a"))
	toAnyone := create(t, s, task(shop, "claude", handoff.Anyone, "b"))
	toCodex := create(t, s, task(shop, "reviewer", "codex", "c"))
	elsewhere := create(t, s, task(blog, "claude", "reviewer", "d"))
	if _, err := s.Take(t.Context(), toAnyone, "codex", "s"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		f    handoff.Filter
		want []int64
	}{
		{"open ones addressed to reviewer", handoff.Filter{Project: shop, Agent: "reviewer", State: handoff.Open}, []int64{toReviewer}},
		{"any state addressed to reviewer", handoff.Filter{Project: shop, Agent: "reviewer"}, []int64{toAnyone, toReviewer}},
		{"created by reviewer", handoff.Filter{Project: shop, Agent: "reviewer", Mine: true}, []int64{toCodex}},
		{"taken ones", handoff.Filter{Project: shop, State: handoff.Taken}, []int64{toAnyone}},
		{"every project", handoff.Filter{Agent: "reviewer", State: handoff.Open}, []int64{elsewhere, toReviewer}},
		{"everything", handoff.Filter{}, []int64{elsewhere, toCodex, toAnyone, toReviewer}},
	} {
		got, err := s.List(t.Context(), tc.f)
		if err != nil || !slices.Equal(ids(got), tc.want) {
			t.Errorf("%s: %v, %v; want %v", tc.name, ids(got), err, tc.want)
		}
	}
}

// Take refuses a handoff addressed to another agent, one that is not open and an id no handoff has;
// Finish refuses an agent that did not take the handoff, and one already done.
func TestTakeAndFinishRefuseWhatTheyMayNot(t *testing.T) {
	s := newStore(t)
	id := create(t, s, task(shop, "claude", "reviewer", "t"))
	if _, err := s.Take(t.Context(), id, "codex", "s"); !errors.Is(err, handoff.ErrRefused) || !strings.Contains(err.Error(), "addressed to reviewer") {
		t.Fatalf("a take by another agent: %v", err)
	}
	if _, err := s.Finish(t.Context(), id, "reviewer", ""); !errors.Is(err, handoff.ErrRefused) {
		t.Fatalf("a finish before the take: %v", err)
	}
	if _, err := s.Take(t.Context(), id, "reviewer", "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Take(t.Context(), id, "reviewer", "s"); !errors.Is(err, handoff.ErrRefused) || !strings.Contains(err.Error(), "not open") {
		t.Fatalf("a second take: %v", err)
	}
	if _, err := s.Finish(t.Context(), id, "claude", ""); !errors.Is(err, handoff.ErrRefused) || !strings.Contains(err.Error(), "only the agent that took it") {
		t.Fatalf("a finish by the creator: %v", err)
	}
	if _, err := s.Finish(t.Context(), id, "reviewer", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(t.Context(), id, "reviewer", ""); !errors.Is(err, handoff.ErrRefused) || !strings.Contains(err.Error(), "already done") {
		t.Fatalf("a second finish: %v", err)
	}
	if _, err := s.Take(t.Context(), 999, "reviewer", "s"); !errors.Is(err, handoff.ErrNotFound) {
		t.Errorf("a take of an unknown id: %v", err)
	}
	if _, err := s.Finish(t.Context(), 999, "reviewer", ""); !errors.Is(err, handoff.ErrNotFound) {
		t.Errorf("a finish of an unknown id: %v", err)
	}
}

// Agents that all see a handoff addressed to anyone may take it at the same moment. The take reads and
// writes in one transaction, so in every round exactly one gets it and every other gets ErrRefused.
func TestTakeRaceHasOneWinner(t *testing.T) {
	s := newStore(t)
	for round := range 5 {
		id := create(t, s, task(shop, "claude", handoff.Anyone, fmt.Sprint("round ", round)))
		var mu sync.Mutex
		var won []string
		refused := 0
		var wg sync.WaitGroup
		for i := range 8 {
			agent := fmt.Sprint("agent-", i)
			wg.Go(func() {
				_, err := s.Take(t.Context(), id, agent, agent+"-session")
				mu.Lock()
				defer mu.Unlock()
				switch {
				case err == nil:
					won = append(won, agent)
				case errors.Is(err, handoff.ErrRefused):
					refused++
				default:
					t.Errorf("round %d, %s: %v", round, agent, err)
				}
			})
		}
		wg.Wait()
		if len(won) != 1 || refused != 7 {
			t.Fatalf("round %d: %v took it and %d were refused, want one taker and 7 refused", round, won, refused)
		}
		taken, err := s.List(t.Context(), handoff.Filter{State: handoff.Taken})
		if err != nil || len(taken) == 0 || taken[0].ID != id || taken[0].TakenBy != won[0] {
			t.Fatalf("round %d: stored %+v, %v; want %s as the taker", round, taken, err, won[0])
		}
	}
}
