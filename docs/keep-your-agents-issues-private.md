# Keep Your Agent's Issues Private

Your coding agent doesn't need a special tool to use a private issue tracker. It needs one paragraph of instructions and a token that can reach the tracker.

## One private repo for every project's issues

I keep my plans out of my public repositories by putting all my issues in a single private repository. Each issue gets a label naming the code repository it belongs to, such as `repo: you/widget`. The code stays public, the backlog stays private, and I have one place to look.

I built a small `gh` wrapper for myself that makes this convention invisible in my terminal. I don't give it to my agents. An agent can follow a label convention from a written instruction, the commands are easier to read in its logs, and the same instruction works on my laptop and in a cloud environment with nothing to install.

## What to tell your agent

Paste this into your `AGENTS.md`, replacing `you/private-issues` with your own repository.

````markdown
## Issues

Issues for all my repositories live in `you/private-issues`. Each issue has a label named `repo: <owner>/<name>` for the code repository it belongs to. Don't create issues in the code repository.

Find the code repository's name:

```bash
gh repo view --json nameWithOwner --jq .nameWithOwner
```

List its issues:

```bash
gh issue list -R you/private-issues --label "repo: <owner>/<name>"
```

Create an issue, creating the label first if it doesn't exist:

```bash
gh label create "repo: <owner>/<name>" -R you/private-issues || true
gh issue create -R you/private-issues --label "repo: <owner>/<name>" --title "..." --body "..."
```

Before working on an issue by number, confirm it has this repository's label.

In commits and pull requests for public repositories, don't link to or quote private issues.
````

The label check matters because issue numbers are shared across all your projects. Issue 17 might belong to a different repository than the one the agent is working in.

## What to watch

- **Exposure.** The agent can read its token. If a malicious web page or file manipulates the agent, it could read or change every issue in the tracker. The token limits that risk to one repository.
- **Cloud agent access.** Many cloud agent environments only grant access to the repository the agent is working in. If you use cloud agents, you may need to take extra steps before they can reach your private issues repository. If you issue a fine-grained token for this, remember that it expires, and agents will start getting 401 errors when it does.
- **Public references.** A closing keyword or issue link in a public pull request reveals your tracker's name. That's why the instructions forbid them.
