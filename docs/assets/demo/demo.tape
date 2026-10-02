# The README's demo. Render it with the demo workflow (.github/workflows/demo.yml), or on Linux from
# the repository root with vhs, ttyd, ffmpeg, sqlite3 and a derbent build on PATH:
#   vhs docs/assets/demo/demo.tape
# It runs in a throwaway HOME, so the config and database are fresh and nothing of yours is touched.

Output docs/assets/demo/demo.gif

Require derbent
Require sqlite3

Set Shell bash
Set FontSize 20
Set Width 1360
Set Height 680
Set Padding 20
Set Framerate 20
Set TypingSpeed 20ms

Hide
Type "mkdir /tmp/derbent-demo && mkdir -p /tmp/derbent-demo/.config/derbent /tmp/derbent-demo/.local/state/derbent /tmp/derbent-demo/app"
Enter
Type "cp docs/assets/demo/config.toml /tmp/derbent-demo/.config/derbent/"
Enter
Type "unset XDG_CONFIG_HOME XDG_STATE_HOME; export HOME=/tmp/derbent-demo PS1='$ ' && cd ~/app && clear"
Enter
Show

Type "cat ~/.config/derbent/config.toml"
Enter
Sleep 3s
Ctrl+L

Type `derbent explain --agent claude --tool native__Bash --args '{"command":"git push origin main"}'`
Enter
Sleep 5s
Ctrl+L

Type "# Claude Code's pre-tool hook sends each shell command to derbent gate"
Enter
Type `call='{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"go test ./..."}}'`
Enter
Type `echo "$call" | derbent gate --agent claude   # allowed: no output, the call runs`
Enter
Sleep 1500ms
Type `call='{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"rm -rf ~/src"}}'`
Enter
Type `echo "$call" | derbent gate --agent claude; echo`
Enter
Sleep 3s
Ctrl+L

Type "derbent receipts"
Enter
Sleep 2s
Type "derbent verify"
Enter
Sleep 2s
Type `sqlite3 ~/.local/state/derbent/derbent.db "UPDATE receipts SET decision='allow' WHERE seq=2"`
Enter
Sleep 500ms
Type "derbent verify"
Enter
Sleep 5s
