# git-review

`git-review` manages a review session independently of the implementation branch.

The work branch remains editable. The reviewed state records explicitly accepted change fragments. A comparison base is pinned when the session starts, so remote branch movement never changes a review session implicitly.

## Commands

```text
git-review --version
git-review -v
git-review init [--base <ref>]
git-review sync-base
git-review status [--json]
git-review accept --path <path> [--path <path>...]
git-review undo
git-review history
git-review reset --commit <commit>
git-review changes --json
git-review fragment inspect --commit <commit> [--path <path>] [--json]
git-review fragment list --json [--unassigned]
git-review group add --name <name> --commit <commit> [--path <path>]
git-review group assign --name <name> --fragment <id> [--fragment <id>...]
git-review group assign --name <name> --commit <commit> [--path <path> --old-start <n> --old-lines <n> --new-start <n> --new-lines <n>]
git-review group list --json
git-review group view --name <name>
git-review group accept --name <name> --path <path>
```

`--version` and `-v` print the CLI version.

`init` enables Git rerere, creates the reviewed-state ref, and stores review-session metadata under `.git/git-review`.

Review units are immutable fragments from a specific commit diff. A fragment is identified by its source commit, file, and source diff coordinates. Its current location is derived from later commits; current line numbers are never used as its identity.

`changes --json` lists unaccepted fragments. `group add` assigns every fragment from a commit, optionally limited to one path. `group assign` can assign one source fragment by its commit, path, and original diff coordinates.

Group path filters accept exact files, directories, and `directory/...` recursive patterns.

`fragment inspect` lists the immutable fragments produced by a commit and gives each one a stable full ID plus a short ID. Those IDs can be assigned later with `group assign --fragment`. `fragment list --unassigned --json` reports review fragments that do not belong to a group.

Groups retain their fragments and statuses. Git hunk formatting is generated for display, but hunk locations are not silently rewritten or discarded when other changes move the file.

Group dependencies are derived from the file histories. Assignments that create a cycle between groups are rejected. A group can be accepted before its dependencies; the acceptance is recorded in session metadata and materialized into the shared reviewed ref once its dependencies are accepted and applied.

`sync-base` merges the current comparison ref into the reviewed state when the work branch already contains that ref, then advances the session's pinned base. `init` installs a post-merge hook that performs this synchronization automatically after the comparison ref is merged into the work branch.

## Development

```text
just build
just test
just install
```

`just install` builds `git-review` into `~/.local/bin` on Linux.
