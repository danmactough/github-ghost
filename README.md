<p align="center">
  <img src="assets/ghost.svg" alt="👻" width="160" height="160">
</p>

<h1 align="center">github-ghost</h1>

<p align="center"><em>One private issues repo, quietly haunting all your projects.</em></p>

A wrapper around the `gh` CLI that makes one private issues repository behave like many repositories. Each repository maps to a label in the issues repository, so `gh issue list` in a checkout of `owner/widget` shows the issues labeled `repo: owner/widget`.

Only `gh issue ...` commands are rewritten. Everything else goes to the real `gh` unchanged.

This tool is for people. To tell your coding agent to use a private issue tracker separate from a public code repo, see [Keep Your Agent's Issues Private](docs/keep-your-agents-issues-private.md).

## 🪄 Install

github-ghost runs on macOS and Linux. You need [`gh`](https://cli.github.com) installed and signed in, and `~/.local/bin` in your `PATH`.

1. Download the latest release for your machine:

   ```bash
   os=$(uname -s | tr '[:upper:]' '[:lower:]')
   arch=$(uname -m | sed 's/x86_64/amd64/; s/aarch64/arm64/')
   gh release download --repo danmactough/github-ghost --pattern "*_${os}_${arch}.tar.gz"
   ```

2. Unpack it and install the binary:

   ```bash
   tar -xzf github-ghost_*_${os}_${arch}.tar.gz
   install -d ~/.local/bin
   install -m 755 github-ghost_*_${os}_${arch}/github-ghost ~/.local/bin/
   ```

3. Run the interactive setup:

   ```bash
   github-ghost ghost --install
   ```

4. Open a new shell.

Setup asks for the three `GH_GHOST_*` settings and writes them to a marked block in `.zshenv` (in `ZDOTDIR` if that is set, otherwise `HOME`). Running it again replaces that block and keeps the rest of the file.

Setup also offers to create a `gh` symlink next to the first `github-ghost` in `PATH` that is outside a git repository, so scripts and coding agents use the wrapper. That directory must come before the real `gh` in `PATH`, and setup warns if it does not. It won't replace an existing `gh` that isn't github-ghost. Once the symlink is in place, `gh ghost --install` also runs setup.

`github-ghost ghost --version` or `-v` prints the version.

## 🎛️ Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `GH_GHOST_ISSUE_REPO` | None | Repository that holds the issues, as `owner/name` |
| `GH_GHOST_LABEL_PREFIX` | `repo: ` | Text before `owner/name` in the label |
| `GH_GHOST_LABEL_COLOR` | Random for each new label | Six hex digits, with or without a leading `#` |
| `GHOST_REAL_GH` | first other `gh` in `PATH` | Path to the real `gh` |

Without `GH_GHOST_ISSUE_REPO`, every command prints a warning on stderr and passes through to `gh` unchanged.

## 👀 Behavior

The target repository comes from `-R`, then `GH_REPO`, then `gh repo set-default`, then the `upstream`, `github`, and `origin` remotes. If there is no GitHub target, the command runs unchanged. It also runs unchanged when the target is the issues repository itself, or when `GH_HOST` names a host other than `github.com`.

| Command | What the wrapper does |
| --- | --- |
| `list`, `create` | Adds the issues repository and the label |
| `view`, `close`, `reopen`, `comment`, `edit`, `delete`, `pin`, `unpin`, `lock`, `unlock`, `transfer` | Adds the issues repository after confirming the issue has this repository's label |
| `status`, `develop` | Returns an error |

For `list` and `create`, a missing label is created if the target repository exists on GitHub. If it doesn't exist, the command fails.

Issue URLs name their own repository, so commands given URLs run unchanged.

## 🚧 Limitations

- Output shows the `repo: ...` label, and headers and URLs show the issues repository.
- `gh api` and `gh search issues` are not rewritten.
- User-defined `gh` aliases are not expanded, so they run unchanged.
- An unknown `gh issue` flag is an error, because the wrapper can't tell whether the next argument is that flag's value. A new `gh` flag needs an entry in the `commands` table in `main.go`.

## 🛠️ Development

Build from source and install to `~/.local/bin`:

```bash
make install
```

Set `PREFIX` or `BINDIR` to install somewhere else. `make build`, `make test`, `make vet`, `make fmt`, `make uninstall`, and `make clean` are also available.

### 🚀 Release

Commit everything on `main`, then run:

```bash
make release VERSION=v0.1.0
```

This runs the checks and tests, builds macOS and Linux archives for arm64 and amd64 in `dist/v0.1.0`, pushes the tag, and creates a GitHub release with the archives and `checksums.txt`.
