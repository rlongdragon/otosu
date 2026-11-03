#!/bin/sh
# UserPromptSubmit and PreToolUse(Read) hook: otosu collects any drop codes in
# the prompt, then lets Claude read the files it wrote. Without an otosu CLI
# that knows claude-hook there is nothing to do, so the hook stays out of the
# way rather than reporting an error on every prompt.

# The installer's default location, in case it is not on PATH here.
PATH="$PATH:$HOME/.local/bin"
command -v otosu >/dev/null 2>&1 || exit 0
otosu claude-hook 2>/dev/null || exit 0
