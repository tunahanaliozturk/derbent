package suggest_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/rule"
	"github.com/tunahanaliozturk/derbent/internal/suggest"
)

// answers makes n answered calls from agent to tool with args, approved or denied, asked by the user's rule
// n.
func answers(n int, agent, tool, args string, approved bool, asked int) []approval.Answered {
	out := make([]approval.Answered, n)
	for i := range out {
		out[i] = approval.Answered{Agent: agent, Tool: tool, Args: args, Rule: asked, Approved: approved}
	}
	return out
}

// shell makes one approved call from claude to native__Bash per command, under the argument key, asked by
// rule 3.
func shell(key string, cmds ...string) []approval.Answered {
	out := make([]approval.Answered, len(cmds))
	for i, c := range cmds {
		args, err := json.Marshal(map[string]string{key: c})
		if err != nil {
			panic(err) // a map of strings always encodes
		}
		out[i] = approval.Answered{Agent: "claude", Tool: "native__Bash", Args: string(args), Rule: 3, Approved: true}
	}
	return out
}

// Answers are grouped by agent and tool. Enough approvals and no denial suggest an allow, enough denials
// and no approval a deny, and fewer, or mixed answers, nothing. Calls a project's rules asked about do not
// count, and the rules that asked are kept, lowest first, for the snippet's comment.
func TestFromGroupsAnswersByAgentAndTool(t *testing.T) {
	var calls []approval.Answered
	calls = append(calls, answers(5, "codex", "github__create_issue", `{"title":"x"}`, true, 4)...)
	calls = append(calls, answers(4, "codex", "github__get_me", `{}`, true, 4)...)
	calls = append(calls, answers(5, "claude", "github__delete_repo", `{}`, false, 2)...)
	calls = append(calls, answers(5, "claude", "memory_write", `{"title":"t"}`, true, 1)...)
	calls = append(calls, answers(1, "claude", "memory_write", `{"title":"t"}`, false, 1)...)
	project := answers(6, "codex", "github__merge_pull_request", `{}`, true, 1)
	for i := range project {
		project[i].ProjectRule = true
	}
	calls = append(calls, project...)
	calls = append(calls, answers(1, "codex", "github__create_issue", `{"title":"y"}`, true, 7)...)
	want := []suggest.Suggestion{
		{Agent: "claude", Tool: "github__delete_repo", Action: rule.Deny, Denied: 5, Rules: []int{2}},
		{Agent: "codex", Tool: "github__create_issue", Action: rule.Allow, Approved: 6, Rules: []int{4, 7}},
	}
	if got := suggest.From(calls, suggest.Min); !reflect.DeepEqual(got, want) {
		t.Fatalf("From =\n%+v\nwant\n%+v", got, want)
	}
	if got := suggest.From(calls, 4); len(got) != 3 || got[2].Tool != "github__get_me" {
		t.Fatalf("From with 4 = %+v, want github__get_me too, last", got)
	}
}

// A suggestion never lets through more than the answered commands start with. Each case would give a wider
// rule if the common start were taken as it is.
func TestSuggestionsNeverWiden(t *testing.T) {
	arrayCall := shell("command", "go test ./...")[0]
	arrayCall.Args = `{"command":["go","test"]}`
	noCommand := shell("command", "go test ./...")[0]
	noCommand.Args = `{"description":"run the tests"}`
	four := shell("command", "go test ./a", "go test ./b", "go test ./c", "go test ./d")
	for _, tc := range []struct {
		name        string
		calls       []approval.Answered
		key, prefix string // no prefix: no suggestion
	}{
		{"the same command", shell("command", "npm test", "npm test", "npm test", "npm test", "npm test"), "command", "npm test"},
		{"a word cut in two", shell("command", "git status", "git stash", "git status", "git stash", "git status"), "command", "git "},
		{"a star in the commands", shell("command", "cat a*b x", "cat a*b y", "cat a*b x", "cat a*b y", "cat a*b x"), "command", "cat "},
		{"a question mark in the commands", shell("command", "ls ?x", "ls ?y", "ls ?x", "ls ?y", "ls ?x"), "command", "ls "},
		{"a UTF-8 character cut in two", shell("command", "echo héllo", "echo hèllo", "echo héllo", "echo hèllo", "echo héllo"), "command", "echo "},
		{"white space only", shell("command", " ls", " cd", " ls", " cd", " ls"), "", ""},
		{"nothing shared", shell("command", "ls", "cd x", "ls", "cd x", "ls"), "", ""},
		{"Antigravity CLI's key", shell("CommandLine", "terraform plan", "terraform plan", "terraform plan", "terraform plan", "terraform plan"), "CommandLine", "terraform plan"},
		{"a command that is not a string", append(four, arrayCall), "", ""},
		{"a shell call without a command", append(shell("command", "go test ./a", "go test ./b", "go test ./c", "go test ./d"), noCommand), "", ""},
	} {
		got := suggest.From(tc.calls, suggest.Min)
		switch {
		case tc.prefix == "" && len(got) != 0:
			t.Errorf("%s: suggested %+v, want nothing", tc.name, got)
		case tc.prefix != "" && (len(got) != 1 || got[0].Key != tc.key || got[0].Prefix != tc.prefix):
			t.Errorf("%s: %+v, want %s = %q followed by *", tc.name, got, tc.key, tc.prefix)
		}
	}
	if got := suggest.From(answers(5, "claude", "native__B*sh", `{}`, true, 1), suggest.Min); len(got) != 0 {
		t.Fatalf("a tool name with a wildcard: %+v", got)
	}
}

// A snippet parses with the config parser and reads back the agent, the tool and the pattern as they were,
// whatever quotes, backslashes, newlines or invisible characters the tool name holds, and decides as
// suggested: the answered command is allowed and a command outside the prefix still asks.
func TestSnippetsReadBackAsWritten(t *testing.T) {
	tool := "native__Say \"hi\"\\\n[[rule]]\naction = \"allow\"\u202e"
	got := suggest.From(answers(5, "claude", tool, `{"command":"git status --short"}`, true, 1), suggest.Min)
	if len(got) != 1 {
		t.Fatalf("From = %+v", got)
	}
	snippet := got[0].TOML()
	if strings.ContainsRune(snippet, '\u202e') || strings.Count(snippet, "\n[[rule]]\n") != 1 {
		t.Fatalf("the snippet holds raw text from the tool name:\n%s", snippet)
	}
	cfg, err := config.Parse("config.toml", snippet+"\n[[rule]]\naction = \"ask\"\n")
	if err != nil {
		t.Fatalf("the snippet does not parse: %v\n%s", err, snippet)
	}
	if d := cfg.Rules.Decide("claude", tool, map[string]any{"command": "git status --short"}); d.Action != rule.Allow || d.Rule != 1 {
		t.Fatalf("the answered command: %+v", d)
	}
	if d := cfg.Rules.Decide("claude", tool, map[string]any{"command": "rm -rf ."}); d.Action != rule.Ask {
		t.Fatalf("a command outside the prefix: %+v", d)
	}
	for _, want := range []string{
		"# claude's native__Say", "approved 5 times and never denied.\n",
		"# Put it above rule 1 in your config, so first match reaches it before rule 1, which asked.\n",
		"args   = { command = \"git status --short*\" }\n", "action = \"allow\"\n",
	} {
		if !strings.Contains(snippet, want) {
			t.Errorf("the snippet lacks %q:\n%s", want, snippet)
		}
	}
}

// hostile holds what could end a TOML string or line early, start a table, or reach a terminal raw: a
// quote, a backslash, a newline, a carriage return, a tab, a table header, an escape sequence with a bell,
// a bidirectional override, a zero-width space, a tag character, DEL and a C1 control.
const hostile = "a\"b\\c\nd\r\te[[rule]]\naction = \"allow\"\x1b]0;x\x07\u202e\u200b\U000e0041\x7f\u0085"

// Every string a snippet holds, the agent label, the tool name and the command prefix, reads back as it
// was, and none of them adds a line, a table or a raw control character to the snippet. No gate writes
// such an agent label, but the database is a file anyone can write.
func TestSnippetStringsCannotBreakOut(t *testing.T) {
	s := suggest.Suggestion{Agent: hostile, Tool: hostile, Action: rule.Deny, Key: "command", Prefix: hostile, Denied: 5, Rules: []int{2, 9}}
	snippet := s.TOML()
	if strings.Count(snippet, "\n") != 7 || strings.ContainsAny(snippet, "\r\t\x1b\x07\u202e\u200b\U000e0041\x7f\u0085") {
		t.Fatalf("the snippet has lines or raw runes of its own:\n%q", snippet)
	}
	var file struct {
		Rule []rule.Spec `toml:"rule"`
	}
	if _, err := toml.Decode(snippet, &file); err != nil {
		t.Fatalf("the snippet does not decode: %v\n%q", err, snippet)
	}
	want := []rule.Spec{{Agent: hostile, Tool: hostile, Args: map[string]string{"command": hostile + "*"}, Action: rule.Deny}}
	if !reflect.DeepEqual(file.Rule, want) {
		t.Fatalf("the snippet reads back as\n%q\nwant\n%q", file.Rule, want)
	}
	if !strings.Contains(snippet, "so first match reaches it before rules 2 and 9, which asked.\n") {
		t.Errorf("the snippet names the rules that asked wrongly:\n%s", snippet)
	}

	// Through From and the config parser, a command holding the same text is allowed as it was answered.
	got := suggest.From(shell("command", hostile, hostile, hostile, hostile, hostile), suggest.Min)
	if len(got) != 1 || got[0].Prefix != hostile {
		t.Fatalf("From = %+v", got)
	}
	cfg, err := config.Parse("config.toml", got[0].TOML()+"\n[[rule]]\naction = \"ask\"\n")
	if err != nil {
		t.Fatalf("the snippet does not parse: %v\n%s", err, got[0].TOML())
	}
	if d := cfg.Rules.Decide("claude", "native__Bash", map[string]any{"command": hostile}); d.Action != rule.Allow || d.Rule != 1 {
		t.Fatalf("the answered command: %+v", d)
	}
	if d := cfg.Rules.Decide("claude", "native__Bash", map[string]any{"command": "a\"b"}); d.Action != rule.Ask {
		t.Fatalf("a command outside the prefix: %+v", d)
	}
}
