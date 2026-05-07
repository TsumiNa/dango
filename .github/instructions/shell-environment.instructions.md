---
description: "Use at the start of any new session before running terminal commands. Detect the active shell to avoid heredoc and syntax errors caused by shell incompatibility (fish, bash, zsh, sh)."
---
# Shell Environment Awareness

At the start of every new agent session, before executing any terminal command, confirm the active shell.

## How to Confirm

Run:

```
echo $SHELL
```

or check `$0` if `$SHELL` is unset. Record the result for the rest of the session.

## Shell-Specific Rules

**fish shell** — does NOT support POSIX heredocs. Avoid all of these patterns:

```bash
# WRONG in fish
cat << 'EOF' >> file.txt
...content...
EOF
```

Use `printf` or `echo` with explicit newlines instead, or write the file directly with an editor tool:

```fish
printf '%s\n' 'line1' 'line2' >> file.txt
```

Or prefer the `create_file` / `replace_string_in_file` agent tools whenever available — they bypass shell syntax entirely.

**bash / zsh / sh** — POSIX heredocs are safe. Use them normally.

## General Rules

- Never assume `bash` without confirming — the user's default interactive shell may differ.
- Do not use `bash -c "..."` sub-shells to work around fish syntax unless explicitly asked.
- When in doubt, prefer agent file-editing tools over shell redirection for writing file content.
- If a command fails with a shell syntax error, check the active shell before retrying.
