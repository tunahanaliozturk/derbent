# 0016. Presets are files written once

Date: 2026-09-28
Status: accepted

## Context

Without a config every call is allowed, and a first config is a blank page. A preset gives a starting
point. It could be a mode Derbent keeps, a line such as `preset = "balanced"` whose rules come from the
binary and change with each release, or a file Derbent writes once and the user owns from then on.

## Decision

- Derbent embeds three commented TOML files: `watch`, `balanced` and `strict`.
  `derbent init --preset <name>` writes one to the default config path only when no file exists there,
  and `derbent init --preset <name> --print` prints it and writes nothing.
- Derbent never changes a preset after writing it, and never replaces an existing config. There is no
  preset mode in the config file.
- `watch` is one rule that allows every call. `strict` allows Derbent's memory tools, each CLI's
  built-in tools that only read, keep notes and plans or ask the user, named per CLI, and twelve known
  read tools of the GitHub server, and asks about every other tool. `balanced` names each CLI's
  read-only, shell and file tools and the argument keys they use, and its comments say what its patterns
  catch, what they miss, and where they catch too much.

## Consequences

- The rules that decide the user's calls are text the user can read and edit, and no release loosens or
  tightens them behind the user's back.
- A better preset in a later release reaches only new configs; `--print` shows the current one to compare
  with.
- The presets are long: rules are first-match globs, and each CLI names its tools and arguments its own
  way, so one idea takes a rule per CLI or per argument name.
