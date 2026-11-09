// github-ghost wraps the gh CLI so that one private issues repository behaves
// like many repositories. Each repository maps to a label in the issues
// repository. Commands other than "gh issue ..." pass through unchanged.
package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

type repo struct{ owner, name string }

func (r repo) String() string { return r.owner + "/" + r.name }

func (r repo) same(other repo) bool { return strings.EqualFold(r.String(), other.String()) }

type config struct {
	issuesRepo  repo
	labelPrefix string
	labelColor  string
}

type apiRequest struct {
	path   string
	fields map[string]string
}

type apiResponse struct {
	status int
	body   []byte
}

type deps struct {
	getenv func(string) string
	api    func(apiRequest) (apiResponse, error)
	git    func(args ...string) (string, error)
}

type issueArgs int

const (
	noIssueArg issueArgs = iota
	firstArgIsIssue
	allArgsAreIssues
)

type command struct {
	valueFlags  string
	boolFlags   string
	issues      issueArgs
	addLabel    bool
	unsupported string
}

// gh's flags are listed per subcommand because a flag's value can look like a
// positional argument or like -R. Guessing wrong would apply a command to the
// wrong issue, so an unknown flag is an error.
var commands = map[string]command{
	"list": {
		valueFlags: "app a assignee A author q jq json l label L limit mention m milestone S search s state t template type",
		boolFlags:  "w web",
		addLabel:   true,
	},
	"create": {
		valueFlags: "a assignee attach blocked-by blocking b body F body-file l label m milestone parent p project recover T template t title type",
		boolFlags:  "e editor w web",
		addLabel:   true,
	},
	"view":   {valueFlags: "q jq json t template", boolFlags: "c comments w web", issues: firstArgIsIssue},
	"close":  {valueFlags: "c comment duplicate-of r reason", issues: firstArgIsIssue},
	"reopen": {valueFlags: "c comment", issues: firstArgIsIssue},
	"comment": {
		valueFlags: "attach b body F body-file",
		boolFlags:  "create-if-none delete-last edit-last e editor w web yes",
		issues:     firstArgIsIssue,
	},
	"edit": {
		valueFlags: "add-assignee add-blocked-by add-blocking add-label add-project add-sub-issue attach b body F body-file m milestone parent remove-assignee remove-blocked-by remove-blocking remove-label remove-project remove-sub-issue t title type",
		boolFlags:  "remove-milestone remove-parent remove-type",
		issues:     allArgsAreIssues,
	},
	"delete":   {boolFlags: "yes", issues: firstArgIsIssue},
	"pin":      {issues: firstArgIsIssue},
	"unpin":    {issues: firstArgIsIssue},
	"lock":     {valueFlags: "r reason", issues: firstArgIsIssue},
	"unlock":   {issues: firstArgIsIssue},
	"transfer": {issues: firstArgIsIssue},
	"status":   {unsupported: "cannot limit it to one repository label; use: gh issue list --assignee @me"},
	"develop":  {unsupported: "it would create the branch in the issues repository"},
}

var subcommandAliases = map[string]string{"ls": "list", "new": "create"}

type parsedArgs struct {
	repoFlag    string
	positionals []string
	kept        []string
	help        bool
}

func hasWord(words, word string) bool {
	for _, candidate := range strings.Fields(words) {
		if candidate == word {
			return true
		}
	}
	return false
}

func parseArgs(cmd command, args []string) (parsedArgs, error) {
	var parsed parsedArgs
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			parsed.kept = append(parsed.kept, args[i:]...)
			parsed.positionals = append(parsed.positionals, args[i+1:]...)
			return parsed, nil
		case arg == "-" || !strings.HasPrefix(arg, "-"):
			parsed.kept = append(parsed.kept, arg)
			parsed.positionals = append(parsed.positionals, arg)
		case strings.HasPrefix(arg, "--"):
			name, value, hasValue := strings.Cut(arg[2:], "=")
			switch {
			case name == "help":
				parsed.help = true
			case name == "repo":
				if !hasValue {
					i++
					if i >= len(args) {
						parsed.help = true
						return parsed, nil
					}
					value = args[i]
				}
				parsed.repoFlag = value
			case hasWord(cmd.valueFlags, name):
				parsed.kept = append(parsed.kept, arg)
				if !hasValue && i+1 < len(args) {
					i++
					parsed.kept = append(parsed.kept, args[i])
				}
			case hasWord(cmd.boolFlags, name):
				parsed.kept = append(parsed.kept, arg)
			default:
				return parsed, fmt.Errorf("unknown flag --%s", name)
			}
		default:
			var err error
			if i, err = parseShortFlags(cmd, args, i, &parsed); err != nil {
				return parsed, err
			}
		}
	}
	return parsed, nil
}

func parseShortFlags(cmd command, args []string, i int, parsed *parsedArgs) (int, error) {
	arg := args[i]
	for j := 1; j < len(arg); j++ {
		flag := arg[j : j+1]
		attached := arg[j+1:]
		switch {
		case flag == "h":
			parsed.help = true
		case flag == "R":
			if j > 1 {
				parsed.kept = append(parsed.kept, arg[:j])
			}
			if attached != "" {
				parsed.repoFlag = strings.TrimPrefix(attached, "=")
				return i, nil
			}
			if i+1 >= len(args) {
				parsed.help = true
				return i, nil
			}
			parsed.repoFlag = args[i+1]
			return i + 1, nil
		case hasWord(cmd.valueFlags, flag):
			parsed.kept = append(parsed.kept, arg)
			if attached == "" && i+1 < len(args) {
				parsed.kept = append(parsed.kept, args[i+1])
				return i + 1, nil
			}
			return i, nil
		case hasWord(cmd.boolFlags, flag):
			if attached == "" {
				parsed.kept = append(parsed.kept, arg)
			}
		default:
			return i, fmt.Errorf("unknown flag -%s", flag)
		}
	}
	return i, nil
}

var repoPart = regexp.MustCompile(`^[\w.-]+$`)
var hexColor = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)

// parseRepo accepts OWNER/REPO, HOST/OWNER/REPO, and https, ssh, and scp-style URLs.
func parseRepo(value string) (repo, string, bool) {
	host := "github.com"
	path := value
	if strings.Contains(value, "://") {
		parsed, err := url.Parse(value)
		if err != nil {
			return repo{}, "", false
		}
		host = parsed.Hostname()
		path = strings.TrimPrefix(parsed.Path, "/")
	} else if before, after, found := strings.Cut(value, ":"); found && strings.Contains(before, "@") {
		_, host, _ = strings.Cut(before, "@")
		path = after
	} else if parts := strings.Split(value, "/"); len(parts) == 3 {
		host = parts[0]
		path = parts[1] + "/" + parts[2]
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(path, "/"), ".git"), "/")
	if len(parts) != 2 || !repoPart.MatchString(parts[0]) || !repoPart.MatchString(parts[1]) {
		return repo{}, "", false
	}
	return repo{owner: parts[0], name: parts[1]}, strings.ToLower(host), true
}

func githubRepo(value string) (repo, bool) {
	target, host, ok := parseRepo(value)
	if !ok || (host != "github.com" && host != "ssh.github.com") {
		return repo{}, false
	}
	return target, true
}

// repoFromGit follows gh's precedence: the default set by "gh repo set-default",
// then the upstream, github, and origin remotes.
func repoFromGit(d deps) (repo, bool) {
	remotes := []string{"upstream", "github", "origin"}
	resolved, _ := d.git("config", "--get-regexp", `^remote\..*\.gh-resolved$`)
	for _, line := range strings.Split(strings.TrimSpace(resolved), "\n") {
		key, value, found := strings.Cut(line, " ")
		if !found {
			continue
		}
		if value != "base" {
			if target, ok := githubRepo(value); ok {
				return target, true
			}
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".gh-resolved")
		remotes = append([]string{name}, remotes...)
	}
	for _, name := range remotes {
		remoteURL, err := d.git("remote", "get-url", name)
		if err != nil {
			continue
		}
		return githubRepo(strings.TrimSpace(remoteURL))
	}
	return repo{}, false
}

func resolveTarget(parsed parsedArgs, d deps) (repo, bool) {
	if parsed.repoFlag != "" {
		return githubRepo(parsed.repoFlag)
	}
	if fromEnv := d.getenv("GH_REPO"); fromEnv != "" {
		return githubRepo(fromEnv)
	}
	return repoFromGit(d)
}

func ensureLabel(cfg config, target repo, label string, d deps) error {
	labelsPath := fmt.Sprintf("repos/%s/labels", cfg.issuesRepo)
	existing, err := d.api(apiRequest{path: labelsPath + "/" + url.PathEscape(label)})
	if err != nil {
		return err
	}
	if existing.status == 200 {
		return nil
	}
	if existing.status != 404 {
		return fmt.Errorf("could not read label %q: HTTP %d", label, existing.status)
	}
	targetRepo, err := d.api(apiRequest{path: "repos/" + target.String()})
	if err != nil {
		return err
	}
	if targetRepo.status == 404 {
		return fmt.Errorf("could not resolve to a repository with the name %q", target.String())
	}
	if targetRepo.status != 200 {
		return fmt.Errorf("could not check whether %s exists: HTTP %d", target, targetRepo.status)
	}
	created, err := d.api(apiRequest{path: labelsPath, fields: map[string]string{"name": label, "color": newLabelColor(cfg)}})
	if err != nil {
		return err
	}
	// 422 means another process created the label after the lookup above.
	if created.status != 201 && created.status != 422 {
		return fmt.Errorf("could not create label %q: HTTP %d", label, created.status)
	}
	return nil
}

func checkIssue(cfg config, target repo, label, number string, d deps) error {
	notFound := fmt.Errorf("could not find issue %s in %s", number, target)
	response, err := d.api(apiRequest{path: fmt.Sprintf("repos/%s/issues/%s", cfg.issuesRepo, number)})
	if err != nil {
		return err
	}
	if response.status == 404 {
		return notFound
	}
	if response.status != 200 {
		return fmt.Errorf("could not read issue %s: HTTP %d", number, response.status)
	}
	var issue struct {
		Labels []struct{ Name string } `json:"labels"`
	}
	if err := json.Unmarshal(response.body, &issue); err != nil {
		return fmt.Errorf("could not parse issue %s: %w", number, err)
	}
	for _, candidate := range issue.Labels {
		if strings.EqualFold(candidate.Name, label) {
			return nil
		}
	}
	return notFound
}

// rewrite returns the arguments to run the real gh with.
func rewrite(args []string, cfg config, d deps) ([]string, error) {
	if len(args) < 2 || args[0] != "issue" {
		return args, nil
	}
	if host := d.getenv("GH_HOST"); host != "" && !strings.EqualFold(host, "github.com") {
		return args, nil
	}
	subcommand := args[1]
	if canonical, ok := subcommandAliases[subcommand]; ok {
		subcommand = canonical
	}
	cmd, known := commands[subcommand]
	if !known {
		return args, nil
	}
	parsed, err := parseArgs(cmd, args[2:])
	if err != nil {
		return nil, fmt.Errorf("gh issue %s: %w", subcommand, err)
	}
	if parsed.help {
		return args, nil
	}
	target, ok := resolveTarget(parsed, d)
	if !ok || target.same(cfg.issuesRepo) {
		return args, nil
	}
	if cmd.unsupported != "" {
		return nil, fmt.Errorf("gh issue %s is not supported: %s", subcommand, cmd.unsupported)
	}

	var issues []string
	switch {
	case cmd.issues == firstArgIsIssue && len(parsed.positionals) > 0:
		issues = parsed.positionals[:1]
	case cmd.issues == allArgsAreIssues:
		issues = parsed.positionals
	}
	urlCount := 0
	for _, issue := range issues {
		if strings.Contains(issue, "://") {
			urlCount++
		}
	}
	if urlCount > 0 {
		// A URL names its own repository, so gh can act on it without rewriting.
		if urlCount == len(issues) {
			return args, nil
		}
		return nil, errors.New("cannot mix issue numbers and URLs")
	}

	label := cfg.labelPrefix + target.String()
	for _, issue := range issues {
		number := strings.TrimPrefix(issue, "#")
		if _, err := strconv.ParseUint(number, 10, 64); err != nil {
			return nil, fmt.Errorf("invalid issue number %q", issue)
		}
		if err := checkIssue(cfg, target, label, number, d); err != nil {
			return nil, err
		}
	}

	rewritten := []string{"issue", args[1], "--repo", cfg.issuesRepo.String()}
	if cmd.addLabel {
		if err := ensureLabel(cfg, target, label, d); err != nil {
			return nil, err
		}
		rewritten = append(rewritten, "--label", label)
	}
	return append(rewritten, parsed.kept...), nil
}

// findRealGH skips this executable so that github-ghost can be installed as a
// gh symlink earlier in PATH.
func findRealGH(getenv func(string) string) (string, error) {
	if explicit := getenv("GHOST_REAL_GH"); explicit != "" {
		return explicit, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	selfInfo, err := os.Stat(self)
	if err != nil {
		return "", err
	}
	for _, dir := range filepath.SplitList(getenv("PATH")) {
		candidate := filepath.Join(dir, "gh")
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 || os.SameFile(info, selfInfo) {
			continue
		}
		return candidate, nil
	}
	return "", errors.New("could not find the real gh in PATH")
}

func parseAPIOutput(output []byte) (apiResponse, bool) {
	normalized := bytes.ReplaceAll(output, []byte("\r\n"), []byte("\n"))
	headers, body, _ := bytes.Cut(normalized, []byte("\n\n"))
	statusLine, _, _ := bytes.Cut(headers, []byte("\n"))
	fields := strings.Fields(string(statusLine))
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return apiResponse{}, false
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return apiResponse{}, false
	}
	return apiResponse{status: status, body: body}, true
}

func newDeps(realGH string) deps {
	return deps{
		getenv: os.Getenv,
		api: func(request apiRequest) (apiResponse, error) {
			args := []string{"api", "--hostname", "github.com", "--include", request.path}
			for key, value := range request.fields {
				args = append(args, "--raw-field", key+"="+value)
			}
			var stdout, stderr bytes.Buffer
			command := exec.Command(realGH, args...)
			command.Stdout = &stdout
			command.Stderr = &stderr
			// gh exits non-zero for HTTP errors, and the status is still in stdout.
			runErr := command.Run()
			if response, ok := parseAPIOutput(stdout.Bytes()); ok {
				return response, nil
			}
			return apiResponse{}, fmt.Errorf("gh api %s: %v: %s", request.path, runErr, strings.TrimSpace(stderr.String()))
		},
		git: func(args ...string) (string, error) {
			output, err := exec.Command("git", args...).Output()
			return string(output), err
		},
	}
}

func loadConfig(getenv func(string) string) (config, error) {
	issueRepo := getenv("GH_GHOST_ISSUE_REPO")
	parsed, _, ok := parseRepo(issueRepo)
	if !ok {
		return config{}, fmt.Errorf("invalid GH_GHOST_ISSUE_REPO %q", issueRepo)
	}
	labelPrefix := getenv("GH_GHOST_LABEL_PREFIX")
	if labelPrefix == "" {
		labelPrefix = defaultLabelPrefix
	}
	labelColor := strings.TrimPrefix(getenv("GH_GHOST_LABEL_COLOR"), "#")
	if labelColor != "" && !hexColor.MatchString(labelColor) {
		return config{}, fmt.Errorf("invalid GH_GHOST_LABEL_COLOR %q: expected six hex digits", labelColor)
	}
	return config{issuesRepo: parsed, labelPrefix: labelPrefix, labelColor: labelColor}, nil
}

// newLabelColor returns the configured color, or a new random one for each label when none is configured.
func newLabelColor(cfg config) string {
	if cfg.labelColor != "" {
		return cfg.labelColor
	}
	color := make([]byte, 3)
	rand.Read(color)
	return hex.EncodeToString(color)
}

func isInGitRepo(dir string) bool {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// findInstalledBinary skips copies inside git repositories, because a PATH entry
// such as ./bin can make a development build look like the installed command.
func findInstalledBinary(pathEnv string) (string, error) {
	for _, dir := range filepath.SplitList(pathEnv) {
		absolute, err := filepath.Abs(filepath.Join(dir, binaryName))
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 || isInGitRepo(filepath.Dir(resolved)) {
			continue
		}
		return resolved, nil
	}
	return "", fmt.Errorf("could not find %s in a PATH directory outside a git repository; install it and run setup again", binaryName)
}

// linkGH creates a gh symlink beside the installed github-ghost binary and returns the messages to print.
func linkGH(pathEnv string) ([]string, error) {
	installed, err := findInstalledBinary(pathEnv)
	if err != nil {
		return nil, err
	}
	installedInfo, err := os.Stat(installed)
	if err != nil {
		return nil, err
	}
	linkPath := filepath.Join(filepath.Dir(installed), "gh")
	messages := []string{}
	if _, err := os.Lstat(linkPath); errors.Is(err, os.ErrNotExist) {
		if err := os.Symlink(binaryName, linkPath); err != nil {
			return nil, err
		}
		messages = append(messages, fmt.Sprintf("Linked %s -> %s", linkPath, binaryName))
	} else if err != nil {
		return nil, err
	} else if info, err := os.Stat(linkPath); err != nil || !os.SameFile(info, installedInfo) {
		return nil, fmt.Errorf("%s already exists and is not github-ghost; remove it first", linkPath)
	} else {
		messages = append(messages, fmt.Sprintf("%s already links to github-ghost", linkPath))
	}

	for _, dir := range filepath.SplitList(pathEnv) {
		// A relative entry depends on the current directory, so it says nothing about other shells.
		if !filepath.IsAbs(dir) {
			continue
		}
		candidate := filepath.Join(dir, "gh")
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		if !os.SameFile(info, installedInfo) {
			messages = append(messages, fmt.Sprintf("warning: %s comes first in PATH, so gh will not use github-ghost; move %s earlier in PATH", candidate, filepath.Dir(linkPath)))
		}
		break
	}
	return messages, nil
}

const (
	binaryName         = "github-ghost"
	defaultLabelPrefix = "repo: "
	zshenvBlockStart   = "# >>> github-ghost >>>"
	zshenvBlockEnd     = "# <<< github-ghost <<<"
)

type installOptions struct {
	in     io.Reader
	out    io.Writer
	getenv func(string) string
}

func promptLine(reader *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := reader.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", errors.New("input ended before setup finished; nothing was changed")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

type valueQuestion struct {
	reader   *bufio.Reader
	out      io.Writer
	question string
	current  string
	validate func(string) (string, error)
}

func askValue(options valueQuestion) (string, error) {
	prompt := options.question + ": "
	if options.current != "" {
		prompt = fmt.Sprintf("%s [%s]: ", options.question, options.current)
	}
	for {
		answer, err := promptLine(options.reader, options.out, prompt)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(answer) == "" {
			answer = options.current
		}
		value, err := options.validate(answer)
		if err == nil {
			return value, nil
		}
		fmt.Fprintln(options.out, err)
	}
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func replaceZshenvBlock(content string, block string) (string, error) {
	start := strings.Index(content, zshenvBlockStart)
	end := strings.Index(content, zshenvBlockEnd)
	if start == -1 && end == -1 {
		if content == "" {
			return block + "\n", nil
		}
		return strings.TrimRight(content, "\n") + "\n\n" + block + "\n", nil
	}
	if start == -1 || end < start {
		return "", fmt.Errorf("found an incomplete %q block; fix it by hand and run setup again", zshenvBlockStart)
	}
	return content[:start] + block + content[end+len(zshenvBlockEnd):], nil
}

// updateZshenv writes through a symlink so a .zshenv kept in a dotfiles repository stays linked.
func updateZshenv(path string, block string) error {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	}
	perm := os.FileMode(0o644)
	content := ""
	if info, err := os.Stat(target); err == nil {
		perm = info.Mode().Perm()
		existing, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		content = string(existing)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	updated, err := replaceZshenvBlock(content, block)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(target), ".zshenv.github-ghost-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	if _, err := temp.WriteString(updated); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(temp.Name(), perm); err != nil {
		return err
	}
	return os.Rename(temp.Name(), target)
}

func runInstall(options installOptions) error {
	reader := bufio.NewReader(options.in)
	out := options.out

	issueRepo, err := askValue(valueQuestion{reader, out, "Issue repository (owner/name)", options.getenv("GH_GHOST_ISSUE_REPO"), func(value string) (string, error) {
		target, ok := githubRepo(strings.TrimSpace(value))
		if !ok {
			return "", errors.New("Enter a GitHub repository as owner/name.")
		}
		return target.String(), nil
	}})
	if err != nil {
		return err
	}

	currentPrefix := options.getenv("GH_GHOST_LABEL_PREFIX")
	if currentPrefix == "" {
		currentPrefix = defaultLabelPrefix
	}
	labelPrefix, err := askValue(valueQuestion{reader, out, "Label prefix", currentPrefix, func(value string) (string, error) {
		return value, nil
	}})
	if err != nil {
		return err
	}

	currentColor := strings.TrimPrefix(options.getenv("GH_GHOST_LABEL_COLOR"), "#")
	if currentColor == "" {
		currentColor = "random"
	}
	labelColor, err := askValue(valueQuestion{reader, out, `Label color (six hex digits, or "random")`, currentColor, func(value string) (string, error) {
		color := strings.TrimPrefix(strings.TrimSpace(value), "#")
		if strings.EqualFold(color, "random") {
			return "", nil
		}
		if !hexColor.MatchString(color) {
			return "", errors.New(`Enter six hex digits, or "random".`)
		}
		return color, nil
	}})
	if err != nil {
		return err
	}

	installSymlink, err := askValue(valueQuestion{reader, out, "Install the gh symlink? (y/n)", "y", func(value string) (string, error) {
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "y", "yes":
			return "y", nil
		case "n", "no":
			return "n", nil
		}
		return "", errors.New("Enter y or n.")
	}})
	if err != nil {
		return err
	}

	zshDir := options.getenv("ZDOTDIR")
	if zshDir == "" {
		zshDir = options.getenv("HOME")
	}
	if zshDir == "" {
		return errors.New("could not find .zshenv: HOME is not set")
	}
	zshenvPath := filepath.Join(zshDir, ".zshenv")
	lines := []string{
		zshenvBlockStart,
		"export GH_GHOST_ISSUE_REPO=" + shellQuote(issueRepo),
		"export GH_GHOST_LABEL_PREFIX=" + shellQuote(labelPrefix),
	}
	if labelColor != "" {
		lines = append(lines, "export GH_GHOST_LABEL_COLOR="+shellQuote(labelColor))
	}
	lines = append(lines, zshenvBlockEnd)
	if err := updateZshenv(zshenvPath, strings.Join(lines, "\n")); err != nil {
		return err
	}
	fmt.Fprintf(out, "Updated %s\n", zshenvPath)

	if installSymlink == "y" {
		messages, err := linkGH(options.getenv("PATH"))
		if err != nil {
			return err
		}
		for _, message := range messages {
			fmt.Fprintln(out, message)
		}
	}
	fmt.Fprintf(out, "Open a new shell, or run: source %s\n", shellQuote(zshenvPath))
	return nil
}

func execGH(realGH string, args []string) error {
	// Replacing this process keeps prompts, pagers, signals, and the exit code exactly as gh handles them.
	return syscall.Exec(realGH, append([]string{"gh"}, args...), os.Environ())
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "ghost" {
		if len(os.Args) != 3 || os.Args[2] != "--install" {
			return errors.New("usage: github-ghost ghost --install")
		}
		return runInstall(installOptions{in: os.Stdin, out: os.Stdout, getenv: os.Getenv})
	}

	realGH, err := findRealGH(os.Getenv)
	if err != nil {
		return err
	}
	if os.Getenv("GH_GHOST_ISSUE_REPO") == "" {
		fmt.Fprintln(os.Stderr, "github-ghost: warning: GH_GHOST_ISSUE_REPO is not set, so this command is passing through to gh unchanged. To set it up, run: github-ghost ghost --install")
		return execGH(realGH, os.Args[1:])
	}
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		return err
	}
	args, err := rewrite(os.Args[1:], cfg, newDeps(realGH))
	if err != nil {
		return err
	}
	return execGH(realGH, args)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "github-ghost: %v\n", err)
		os.Exit(1)
	}
}
