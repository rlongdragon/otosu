---
description: Collect an otosu drop and look at the file
argument-hint: '<code#key> [what to do with it]'
allowed-tools: Bash(otosu claude-hook:*), Read
---

!`otosu claude-hook -text "$0"`

First read the collected file listed above with the Read tool. Then answer the
user's request about it, which follows the code below. If there is no request,
say in one sentence what the file is and wait for instructions.

User input: $ARGUMENTS
