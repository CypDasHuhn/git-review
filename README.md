# git-review

`git-review` manages a review session independently of the implementation branch.

The work branch remains editable. The reviewed state records only explicitly accepted changes. A comparison base is pinned when the session starts, so remote branch movement never changes a review session implicitly.

## Commands

```text
git-review init [--base <ref>]
git-review sync-base
git-review status [--json]
```

`init` enables Git rerere in the current repository, creates the reviewed-state ref, and stores review-session metadata under `.git/git-review`.

`sync-base` merges the current comparison ref into the reviewed state when the work branch already contains that ref, then advances the session's pinned base. `init` installs a post-merge hook that performs this synchronization automatically after the comparison ref is merged into the work branch.

## Development

```text
just build
just test
just install
```

`just install` builds `git-review` into `~/.local/bin` on Linux.
