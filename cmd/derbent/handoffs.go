package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/tunahanaliozturk/derbent/internal/handoff"
	"github.com/tunahanaliozturk/derbent/internal/store"
	"github.com/tunahanaliozturk/derbent/internal/visible"
)

// handoffLine is one handoff as derbent handoffs --json prints it.
type handoffLine struct {
	ID           int64    `json:"id"`
	Project      string   `json:"project"`
	From         string   `json:"from"`
	FromSession  string   `json:"from_session"`
	To           string   `json:"to"`
	Title        string   `json:"title"`
	Body         string   `json:"body"`
	Tags         []string `json:"tags"`
	State        string   `json:"state"`
	TakenBy      string   `json:"taken_by"`
	TakenSession string   `json:"taken_session"`
	Note         string   `json:"note"`
	Created      string   `json:"created"`
	Taken        string   `json:"taken,omitempty"`
	Done         string   `json:"done,omitempty"`
}

// runHandoffs lists the handoffs of every project and agent, newest first: the open ones, or every state
// with --all, as rows or JSON lines, with text from agents escaped. It opens the database read-only.
func runHandoffs(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("handoffs", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dbPath := flags.String("db", "", "database file (default: derbent.db in the user state directory)")
	asJSON := flags.Bool("json", false, "print JSON lines instead of rows")
	all := flags.Bool("all", false, "list taken and done handoffs too, not only the open ones")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("handoffs: unexpected argument %q", flags.Arg(0))
	}
	path, err := databasePath(*dbPath)
	if err != nil {
		return err
	}
	db, err := store.OpenExisting(ctx, path)
	if err != nil {
		return err
	}
	defer db.Close()
	f, none := handoff.Filter{State: handoff.Open}, "no open handoffs"
	if *all {
		f.State, none = "", "no handoffs"
	}
	list, err := handoff.NewStore(db).List(ctx, f)
	if err != nil {
		return err
	}
	if *asJSON {
		for _, h := range list {
			var line []byte
			if line, err = json.Marshal(handoffLineOf(h)); err != nil {
				return err
			}
			if _, err = fmt.Fprintln(stdout, escapeRaw(string(line))); err != nil {
				return err
			}
		}
		return nil
	}
	if len(list) == 0 {
		_, err = fmt.Fprintln(stdout, none)
		return err
	}
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATE\tFROM\tTO\tTAKEN BY\tCREATED\tPROJECT\tTITLE")
	for _, h := range list {
		fmt.Fprintf(w, "#%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", h.ID, visible.Escape(string(h.State)), visible.Escape(h.From),
			visible.Escape(h.To), visible.Escape(cmp.Or(h.TakenBy, "-")), h.Created.Local().Format("2006-01-02 15:04:05"),
			visible.Escape(h.Project), visible.Escape(h.Title))
	}
	return w.Flush()
}

// handoffLineOf is h as a JSON line of derbent handoffs holds it.
func handoffLineOf(h handoff.Handoff) handoffLine {
	tags := h.Tags
	if tags == nil {
		tags = []string{}
	}
	l := handoffLine{
		ID: h.ID, Project: h.Project, From: h.From, FromSession: h.FromSession, To: h.To, Title: h.Title, Body: h.Body,
		Tags: tags, State: string(h.State), TakenBy: h.TakenBy, TakenSession: h.TakenSession, Note: h.Note,
		Created: h.Created.UTC().Format(time.RFC3339Nano),
	}
	if !h.Taken.IsZero() {
		l.Taken = h.Taken.UTC().Format(time.RFC3339Nano)
	}
	if !h.Done.IsZero() {
		l.Done = h.Done.UTC().Format(time.RFC3339Nano)
	}
	return l
}
