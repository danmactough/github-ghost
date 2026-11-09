package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const private = "me/private-issues"

type fakeGitHub struct {
	labels  map[string]bool
	repos   map[string]int
	issues  map[string]string
	created []map[string]string
	calls   []string
}

func (f *fakeGitHub) api(request apiRequest) (apiResponse, error) {
	f.calls = append(f.calls, request.path)
	prefix := "repos/" + private
	switch {
	case request.path == prefix+"/labels":
		f.created = append(f.created, request.fields)
		return apiResponse{status: 201}, nil
	case strings.HasPrefix(request.path, prefix+"/labels/"):
		if f.labels[strings.TrimPrefix(request.path, prefix+"/labels/")] {
			return apiResponse{status: 200}, nil
		}
		return apiResponse{status: 404}, nil
	case strings.HasPrefix(request.path, prefix+"/issues/"):
		body, ok := f.issues[strings.TrimPrefix(request.path, prefix+"/issues/")]
		if !ok {
			return apiResponse{status: 404}, nil
		}
		return apiResponse{status: 200, body: []byte(body)}, nil
	}
	if status, ok := f.repos[strings.TrimPrefix(request.path, "repos/")]; ok {
		return apiResponse{status: status}, nil
	}
	return apiResponse{status: 404}, nil
}

func newFake() *fakeGitHub {
	return &fakeGitHub{
		labels: map[string]bool{"repo:%20x%2Falpha": true},
		repos:  map[string]int{"x/fresh": 200, "x/broken": 500},
		issues: map[string]string{
			"1": `{"labels":[{"name":"bug"},{"name":"repo: x/alpha"}]}`,
			"2": `{"labels":[{"name":"repo: x/beta"}]}`,
		},
	}
}

func testDeps(fake *fakeGitHub, env map[string]string, remotes map[string]string) deps {
	return deps{
		getenv: func(key string) string { return env[key] },
		api:    fake.api,
		git: func(args ...string) (string, error) {
			if args[0] == "config" {
				return env["gh-resolved"], nil
			}
			if remote, ok := remotes[args[2]]; ok {
				return remote + "\n", nil
			}
			return "", errors.New("no such remote")
		},
	}
}

var testConfig = config{issuesRepo: repo{owner: "me", name: "private-issues"}, labelPrefix: "repo: ", labelColor: "abc123"}

func split(command string) []string { return strings.Fields(command) }

func TestRewrite(t *testing.T) {
	scoped := "issue list --repo me/private-issues --label"
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"list with -R", split("issue list -R x/alpha"), append(split(scoped), "repo: x/alpha")},
		{"list alias", split("issue ls --repo=x/alpha"), append(split("issue ls --repo me/private-issues --label"), "repo: x/alpha")},
		{"attached short repo", split("issue list -Rx/alpha"), append(split(scoped), "repo: x/alpha")},
		{"keeps other flags", split("issue list -R x/alpha -l bug --limit 3"), append(split(scoped), "repo: x/alpha", "-l", "bug", "--limit", "3")},
		{"flag value that looks like -R", split("issue list --search -R -R x/alpha"), append(split(scoped), "repo: x/alpha", "--search", "-R")},
		{"host-qualified repo", split("issue list -R github.com/x/alpha"), append(split(scoped), "repo: x/alpha")},
		{"url repo", split("issue list -R https://github.com/x/alpha.git"), append(split(scoped), "repo: x/alpha")},
		{"view own issue", split("issue view 1 -R x/alpha --json title"), split("issue view --repo me/private-issues 1 --json title")},
		{"hash number", split("issue close #1 -R x/alpha"), split("issue close --repo me/private-issues #1")},
		{"flag before number", split("issue edit -t new 1 -R x/alpha"), split("issue edit --repo me/private-issues -t new 1")},
		{"non-issue command", split("pr list -R x/alpha"), split("pr list -R x/alpha")},
		{"help", split("issue list --help"), split("issue list --help")},
		{"issues repository itself", split("issue list -R me/private-issues"), split("issue list -R me/private-issues")},
		{"non-GitHub repo", split("issue list -R gitlab.example.com/x/alpha"), split("issue list -R gitlab.example.com/x/alpha")},
		{"issue URL", split("issue view https://github.com/x/y/issues/9 -R x/alpha"), split("issue view https://github.com/x/y/issues/9 -R x/alpha")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := rewrite(test.args, testConfig, testDeps(newFake(), nil, nil))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("got %q, want %q", got, test.want)
			}
		})
	}
}

func TestTargetResolution(t *testing.T) {
	want := append(split("issue list --repo me/private-issues --label"), "repo: x/alpha")
	tests := []struct {
		name    string
		env     map[string]string
		remotes map[string]string
	}{
		{"GH_REPO", map[string]string{"GH_REPO": "x/alpha"}, nil},
		{"origin ssh", nil, map[string]string{"origin": "git@github.com:x/alpha.git"}},
		{"origin https", nil, map[string]string{"origin": "https://github.com/x/alpha"}},
		{"upstream beats origin", nil, map[string]string{"origin": "git@github.com:me/fork.git", "upstream": "git@github.com:x/alpha.git"}},
		{"gh-resolved base", map[string]string{"gh-resolved": "remote.mine.gh-resolved base"}, map[string]string{"origin": "git@github.com:x/other.git", "mine": "git@github.com:x/alpha.git"}},
		{"gh-resolved name", map[string]string{"gh-resolved": "remote.origin.gh-resolved x/alpha"}, map[string]string{"origin": "git@github.com:x/other.git"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := rewrite(split("issue list"), testConfig, testDeps(newFake(), test.env, test.remotes))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %q, want %q", got, want)
			}
		})
	}
}

func TestPassesThroughWithoutGitHubTarget(t *testing.T) {
	args := split("issue list")
	cases := map[string]deps{
		"no remotes": testDeps(newFake(), nil, nil),
		"gitlab":     testDeps(newFake(), nil, map[string]string{"origin": "git@gitlab.com:x/alpha.git"}),
		"GH_HOST":    testDeps(newFake(), map[string]string{"GH_HOST": "ghe.example.com", "GH_REPO": "x/alpha"}, nil),
	}
	for name, d := range cases {
		got, err := rewrite(args, testConfig, d)
		if err != nil || !reflect.DeepEqual(got, args) {
			t.Fatalf("%s: got %q, %v", name, got, err)
		}
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		message string
	}{
		{"other repository's issue", split("issue view 2 -R x/alpha"), "could not find issue 2"},
		{"same name under another owner", split("issue view 1 -R y/alpha"), "could not find issue 1"},
		{"missing issue", split("issue view 99 -R x/alpha"), "could not find issue 99"},
		{"one bad issue in edit", split("issue edit 1 2 -R x/alpha -t new"), "could not find issue 2"},
		{"unknown flag", split("issue list --brand-new x -R x/alpha"), "unknown flag --brand-new"},
		{"unknown short flag", split("issue view 1 -Z -R x/alpha"), "unknown flag -Z"},
		{"status", split("issue status -R x/alpha"), "not supported"},
		{"develop", split("issue develop 1 -R x/alpha"), "not supported"},
		{"nonexistent repository", split("issue list -R x/missing"), "could not resolve to a repository"},
		{"repository check fails", split("issue list -R x/broken"), "HTTP 500"},
		{"mixed numbers and URLs", split("issue edit 1 https://github.com/x/y/issues/2 -R x/alpha"), "cannot mix"},
		{"invalid number", split("issue view abc -R x/alpha"), "invalid issue number"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := newFake()
			_, err := rewrite(test.args, testConfig, testDeps(fake, nil, nil))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("got %v, want %q", err, test.message)
			}
			if len(fake.created) != 0 {
				t.Fatalf("created labels: %v", fake.created)
			}
		})
	}
}

func TestCreatesMissingLabelWhenRepositoryExists(t *testing.T) {
	fake := newFake()
	got, err := rewrite(split("issue create -R x/fresh -t title -b body"), testConfig, testDeps(fake, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	want := append(split("issue create --repo me/private-issues --label"), "repo: x/fresh", "-t", "title", "-b", "body")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	if !reflect.DeepEqual(fake.created, []map[string]string{{"name": "repo: x/fresh", "color": "abc123"}}) {
		t.Fatalf("created %v", fake.created)
	}
}

func TestExistingLabelSkipsRepositoryCheck(t *testing.T) {
	fake := newFake()
	if _, err := rewrite(split("issue list -R x/alpha"), testConfig, testDeps(fake, nil, nil)); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("calls: %v", fake.calls)
	}
}

func TestParseAPIOutput(t *testing.T) {
	response, ok := parseAPIOutput([]byte("HTTP/2.0 404 Not Found\r\nA: b\r\n\r\n{\"message\":\"x\"}"))
	if !ok || response.status != 404 || string(response.body) != `{"message":"x"}` {
		t.Fatalf("got %+v, %v", response, ok)
	}
	if _, ok := parseAPIOutput([]byte("")); ok {
		t.Fatal("empty output parsed")
	}
}

func TestRandomColorForEachLabelWhenUnset(t *testing.T) {
	cfg := testConfig
	cfg.labelColor = ""
	first := newLabelColor(cfg)
	if !regexp.MustCompile(`^[0-9a-f]{6}$`).MatchString(first) {
		t.Fatalf("got %q", first)
	}
	for range 20 {
		if newLabelColor(cfg) != first {
			return
		}
	}
	t.Fatal("color did not change")
}

func TestLoadConfig(t *testing.T) {
	load := func(env map[string]string) (config, error) {
		return loadConfig(func(key string) string { return env[key] })
	}

	if _, err := load(nil); err == nil || !strings.Contains(err.Error(), "invalid GH_GHOST_ISSUE_REPO") {
		t.Fatalf("got %v", err)
	}
	if _, err := load(map[string]string{"GH_GHOST_ISSUE_REPO": "nope"}); err == nil {
		t.Fatal("accepted invalid repository")
	}
	if _, err := load(map[string]string{"GH_GHOST_ISSUE_REPO": "me/i", "GH_GHOST_LABEL_COLOR": "red"}); err == nil {
		t.Fatal("accepted invalid color")
	}

	defaults, err := load(map[string]string{"GH_GHOST_ISSUE_REPO": "me/i"})
	if err != nil {
		t.Fatal(err)
	}
	want := config{issuesRepo: repo{owner: "me", name: "i"}, labelPrefix: "repo: "}
	if defaults != want {
		t.Fatalf("got %+v", defaults)
	}

	custom, err := load(map[string]string{"GH_GHOST_ISSUE_REPO": "me/i", "GH_GHOST_LABEL_PREFIX": "p/", "GH_GHOST_LABEL_COLOR": "#ABC123"})
	if err != nil {
		t.Fatal(err)
	}
	want = config{issuesRepo: repo{owner: "me", name: "i"}, labelPrefix: "p/", labelColor: "ABC123"}
	if custom != want {
		t.Fatalf("got %+v", custom)
	}
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestLinkCreatesSymlinkAndIsRepeatable(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "github-ghost")
	writeExecutable(t, self)

	messages, err := linkGH(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || !strings.HasPrefix(messages[0], "Linked") {
		t.Fatalf("messages: %q", messages)
	}
	target, err := os.Readlink(filepath.Join(dir, "gh"))
	if err != nil || target != "github-ghost" {
		t.Fatalf("got %q, %v", target, err)
	}

	messages, err = linkGH(dir)
	if err != nil || !strings.Contains(messages[0], "already links") {
		t.Fatalf("got %q, %v", messages, err)
	}
}

func TestLinkRefusesToReplaceAnotherGH(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "github-ghost")
	writeExecutable(t, self)
	realGH := filepath.Join(dir, "gh")
	writeExecutable(t, realGH)

	if _, err := linkGH(dir); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("got %v", err)
	}
	if info, err := os.Lstat(realGH); err != nil || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("gh was changed: %v, %v", info, err)
	}
}

func TestLinkWarnsWhenAnotherGHComesFirst(t *testing.T) {
	dir := t.TempDir()
	earlier := t.TempDir()
	self := filepath.Join(dir, "github-ghost")
	writeExecutable(t, self)
	writeExecutable(t, filepath.Join(earlier, "gh"))

	messages, err := linkGH(earlier + string(os.PathListSeparator) + dir)
	if err != nil || len(messages) != 2 || !strings.Contains(messages[1], "comes first in PATH") {
		t.Fatalf("got %q, %v", messages, err)
	}
}

func TestLinkFailsWhenNotInstalledInPath(t *testing.T) {
	if _, err := linkGH(t.TempDir()); err == nil || !strings.Contains(err.Error(), "could not find github-ghost") {
		t.Fatalf("got %v", err)
	}
}

func newGitRepoBinary(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(repo, "bin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "github-ghost")
	writeExecutable(t, binary)
	return binary
}

func TestLinkSkipsCopiesInGitRepositories(t *testing.T) {
	binary := newGitRepoBinary(t)
	installed := t.TempDir()
	writeExecutable(t, filepath.Join(installed, "github-ghost"))

	if _, err := linkGH(filepath.Dir(binary) + string(os.PathListSeparator) + installed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(installed, "gh")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(binary), "gh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("linked inside the git repository: %v", err)
	}
}

func TestLinkSkipsSymlinkThatResolvesIntoGitRepository(t *testing.T) {
	binary := newGitRepoBinary(t)
	dir := t.TempDir()
	if err := os.Symlink(binary, filepath.Join(dir, "github-ghost")); err != nil {
		t.Fatal(err)
	}
	if _, err := linkGH(dir); err == nil || !strings.Contains(err.Error(), "outside a git repository") {
		t.Fatalf("got %v", err)
	}
}

type installRun struct {
	dir    string
	zshenv string
	output string
	err    error
}

func runInstallWith(t *testing.T, dir string, input string, env map[string]string) installRun {
	t.Helper()
	self := filepath.Join(dir, "github-ghost")
	if _, err := os.Stat(self); err != nil {
		writeExecutable(t, self)
	}
	merged := map[string]string{"HOME": dir, "PATH": dir}
	for key, value := range env {
		merged[key] = value
	}
	var out bytes.Buffer
	err := runInstall(installOptions{
		in:     strings.NewReader(input),
		out:    &out,
		getenv: func(key string) string { return merged[key] },
	})
	return installRun{dir: dir, zshenv: filepath.Join(dir, ".zshenv"), output: out.String(), err: err}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func TestInstallWritesChoicesAndSymlink(t *testing.T) {
	result := runInstallWith(t, t.TempDir(), "me/issues\nproj-\n#ABC123\ny\n", nil)
	if result.err != nil {
		t.Fatal(result.err)
	}
	want := "# >>> github-ghost >>>\nexport GH_GHOST_ISSUE_REPO='me/issues'\nexport GH_GHOST_LABEL_PREFIX='proj-'\nexport GH_GHOST_LABEL_COLOR='ABC123'\n# <<< github-ghost <<<\n"
	if got := readFile(t, result.zshenv); got != want {
		t.Fatalf("got %q", got)
	}
	if target, err := os.Readlink(filepath.Join(result.dir, "gh")); err != nil || target != "github-ghost" {
		t.Fatalf("got %q, %v", target, err)
	}
}

func TestInstallDefaultsKeepPrefixSpaceAndOmitRandomColor(t *testing.T) {
	result := runInstallWith(t, t.TempDir(), "me/issues\n\n\nn\n", nil)
	if result.err != nil {
		t.Fatal(result.err)
	}
	content := readFile(t, result.zshenv)
	if !strings.Contains(content, "export GH_GHOST_LABEL_PREFIX='repo: '\n") || strings.Contains(content, "LABEL_COLOR") {
		t.Fatalf("got %q", content)
	}
	if _, err := os.Lstat(filepath.Join(result.dir, "gh")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlink should not exist: %v", err)
	}
}

func TestInstallUsesCurrentValuesAsDefaults(t *testing.T) {
	env := map[string]string{"GH_GHOST_ISSUE_REPO": "me/current", "GH_GHOST_LABEL_PREFIX": "p-", "GH_GHOST_LABEL_COLOR": "112233"}
	result := runInstallWith(t, t.TempDir(), "\n\n\nn\n", env)
	if result.err != nil {
		t.Fatal(result.err)
	}
	content := readFile(t, result.zshenv)
	for _, want := range []string{"'me/current'", "'p-'", "'112233'"} {
		if !strings.Contains(content, want) {
			t.Fatalf("missing %s in %q", want, content)
		}
	}
}

func TestInstallAsksAgainAfterInvalidAnswers(t *testing.T) {
	result := runInstallWith(t, t.TempDir(), "\nnot a repo\nme/issues\n\nred\nrandom\nmaybe\nn\n", nil)
	if result.err != nil {
		t.Fatal(result.err)
	}
	for _, want := range []string{"Enter a GitHub repository", "Enter six hex digits", "Enter y or n"} {
		if !strings.Contains(result.output, want) {
			t.Fatalf("missing %q in %q", want, result.output)
		}
	}
}

func TestInstallReplacesBlockAndKeepsOtherLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".zshenv")
	if err := os.WriteFile(path, []byte("export BEFORE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runInstallWith(t, dir, "me/one\n\n\nn\n", nil)
	content := readFile(t, path) + "export AFTER=1\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := runInstallWith(t, dir, "me/two\n\n\nn\n", nil); result.err != nil {
		t.Fatal(result.err)
	}
	got := readFile(t, path)
	if strings.Contains(got, "me/one") || strings.Count(got, zshenvBlockStart) != 1 {
		t.Fatalf("got %q", got)
	}
	if !strings.HasPrefix(got, "export BEFORE=1\n") || !strings.HasSuffix(got, "export AFTER=1\n") {
		t.Fatalf("got %q", got)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v, %v", info, err)
	}
}

func TestInstallWritesThroughSymlinkedZshenv(t *testing.T) {
	dir := t.TempDir()
	dotfile := filepath.Join(t.TempDir(), "zshenv")
	if err := os.WriteFile(dotfile, []byte("export KEEP=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(dotfile, filepath.Join(dir, ".zshenv")); err != nil {
		t.Fatal(err)
	}
	if result := runInstallWith(t, dir, "me/issues\n\n\nn\n", nil); result.err != nil {
		t.Fatal(result.err)
	}
	if info, err := os.Lstat(filepath.Join(dir, ".zshenv")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink replaced: %v, %v", info, err)
	}
	if got := readFile(t, dotfile); !strings.Contains(got, "export KEEP=1") || !strings.Contains(got, "me/issues") {
		t.Fatalf("got %q", got)
	}
}

func TestInstallUsesZdotdir(t *testing.T) {
	zdot := t.TempDir()
	result := runInstallWith(t, t.TempDir(), "me/issues\n\n\nn\n", map[string]string{"ZDOTDIR": zdot})
	if result.err != nil {
		t.Fatal(result.err)
	}
	if !strings.Contains(readFile(t, filepath.Join(zdot, ".zshenv")), "me/issues") {
		t.Fatal("ZDOTDIR .zshenv was not written")
	}
}

func TestInstallChangesNothingWhenInputEnds(t *testing.T) {
	result := runInstallWith(t, t.TempDir(), "me/issues\n", nil)
	if result.err == nil || !strings.Contains(result.err.Error(), "nothing was changed") {
		t.Fatalf("got %v", result.err)
	}
	if _, err := os.Stat(result.zshenv); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".zshenv should not exist: %v", err)
	}
}

func TestInstallRefusesIncompleteBlock(t *testing.T) {
	if _, err := replaceZshenvBlock(zshenvBlockStart+"\nexport X=1\n", "new"); err == nil {
		t.Fatal("accepted incomplete block")
	}
}

func TestShellQuote(t *testing.T) {
	if got := shellQuote("it's $HOME"); got != `'it'\''s $HOME'` {
		t.Fatalf("got %s", got)
	}
}
