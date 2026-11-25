package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/miyako/4d-static-analysis/components/internal/ojson"
	"github.com/miyako/4d-static-analysis/components/internal/provider"
	"github.com/miyako/4d-static-analysis/components/internal/resolver"
	"github.com/miyako/4d-static-analysis/components/internal/version"
)

// testTransport is a test seam: tests route provider traffic to a mock server. It is nil in production.
var testTransport http.RoundTripper

type writer = interface{ Write([]byte) (int, error) }

// flags are shared by the commands. Credentials are deliberately not accepted as arguments: a process argument
// is visible to every user of the machine. They come from the environment or a token file.
type flags struct {
	project, runtime, runtimeVersion, runtimePath, home, depsFolder, archiveStore string
	tokenFile, gitlabTokenFile, user, rewrite, output, apiURL                     string
	componentsDir, workspace                                                      string
	update, offline, noWriteLock, allowIncomplete, jsonOut, quiet                 bool
	timeout                                                                       time.Duration
}

func (f *flags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.project, "project", "", "package folder or .4DProject file (or give it as the argument)")
	fs.StringVar(&f.runtime, "runtime", "tool4d", "tool4d | 4D | 4D Server (the cache folder name)")
	fs.StringVar(&f.runtimeVersion, "runtime-version", "", `e.g. 21R5 or 21.2; anchors "version": "4d"`)
	fs.StringVar(&f.runtimePath, "runtime-path", "", "runtime executable, queried with --version when --runtime-version is absent")
	fs.StringVar(&f.home, "home", "", "isolated profile root: the cache goes inside it")
	fs.StringVar(&f.depsFolder, "dependencies-folder", "", "explicit Dependencies folder (overrides the --home location)")
	fs.StringVar(&f.archiveStore, "archive-store", "", "verified archive store; 'none' disables it (default: on with --home)")
	fs.StringVar(&f.tokenFile, "token-file", "", `JSON {"token": ...} like 4D's github.json; otherwise $`+provider.GitHubTokenEnv)
	fs.StringVar(&f.gitlabTokenFile, "gitlab-token-file", "", `JSON {"token": ...} like 4D's gitlab.json; otherwise $`+provider.GitLabTokenEnv+" (gitlab.com only)")
	fs.BoolVar(&f.update, "update", false, "ignore tags pinned in dependencies-lock.json")
	fs.BoolVar(&f.offline, "offline", false, "no network: cached or archived tags only")
	fs.BoolVar(&f.noWriteLock, "no-write-lock", false, "do not write dependencies-lock.json")
	fs.StringVar(&f.user, "user", "", "name for userPreferences.<user> (default: current user)")
	fs.StringVar(&f.rewrite, "rewrite", "", "workspace package copy that receives an exact-tag dependencies.json (tool4d)")
	fs.StringVar(&f.output, "output", "", "write the JSON report here (default: stdout)")
	fs.StringVar(&f.apiURL, "github-api-url", "", "GitHub API base URL (GitHub Enterprise; https, or http on a loopback host for tests)")
	fs.DurationVar(&f.timeout, "timeout", 60*time.Second, "timeout of each HTTP request")
	fs.BoolVar(&f.allowIncomplete, "allow-incomplete", false, "exit 0 even when a dependency is incomplete (the report still says so)")
	fs.BoolVar(&f.quiet, "quiet", false, "do not print warnings and incomplete dependencies on stderr")
}

// parse accepts the project as the first or last positional argument.
func parseArgs(fs *flag.FlagSet, args []string, f *flags) error {
	var positional []string
	for len(args) > 0 {
		if strings.HasPrefix(args[0], "-") {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	positional = append(positional, fs.Args()...)
	if len(positional) > 1 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(positional[1:], " "))
	}
	if len(positional) == 1 {
		if f.project != "" {
			return fmt.Errorf("give the project once")
		}
		f.project = positional[0]
	}
	return nil
}

func readTokenFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	v, err := ojson.Parse(ojson.StripBOM(data))
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %v", path, err)
	}
	if o, ok := v.(*ojson.OMap); ok {
		if t, ok := o.Val("token").(string); ok {
			return t, nil
		}
	}
	return "", nil
}

// The ambient GITHUB_TOKEN and GH_TOKEN are deliberately ignored: a token that an organisation rejects (for
// example SAML) would otherwise break downloads of public components.
func (f *flags) tokens() (github, gitlab string, err error) {
	if f.tokenFile != "" {
		if github, err = readTokenFile(f.tokenFile); err != nil {
			return
		}
	} else {
		github = os.Getenv(provider.GitHubTokenEnv)
	}
	if f.gitlabTokenFile != "" {
		gitlab, err = readTokenFile(f.gitlabTokenFile)
	} else {
		gitlab = os.Getenv(provider.GitLabTokenEnv)
	}
	return
}

var versionOutput = regexp.MustCompile(`\b(\d+(?:\s*R\d+)?(?:\.\d+)?)\s+build\b`)

func detectVersion(binary string) (string, error) {
	cmd := exec.Command(binary, "--version")
	done := make(chan struct{})
	var out []byte
	var err error
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		return "", &version.UsageError{Msg: fmt.Sprintf("cannot query %s --version: timed out", binary)}
	}
	if err != nil && len(out) == 0 {
		return "", &version.UsageError{Msg: fmt.Sprintf("cannot query %s --version: %v", binary, err)}
	}
	m := versionOutput.FindStringSubmatch(string(out))
	if m == nil {
		return "", &version.UsageError{Msg: fmt.Sprintf("cannot parse the version printed by %s", binary)}
	}
	return strings.ReplaceAll(m[1], " ", ""), nil
}

// enforceRunnerPolicy: GitHub-hosted runners cannot run 4D or 4D Server, so only tool4d is allowed there.
func enforceRunnerPolicy(runtime string) error {
	if os.Getenv("GITHUB_ACTIONS") == "true" && os.Getenv("RUNNER_ENVIRONMENT") == "github-hosted" && runtime != "tool4d" {
		return &version.UsageError{Msg: fmt.Sprintf("runtime %q is only allowed on self-hosted runners; GitHub-hosted runners must use tool4d", runtime)}
	}
	return nil
}

func loopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]"
}

// manager builds the resolver with real providers.
func (f *flags) manager(planOnly bool) (*resolver.Manager, error) {
	canonical, ok := resolver.Runtimes[strings.ToLower(strings.TrimSpace(f.runtime))]
	if !ok {
		return nil, &version.UsageError{Msg: fmt.Sprintf("unknown runtime %q; use tool4d, 4D or 4D Server", f.runtime)}
	}
	if err := enforceRunnerPolicy(canonical); err != nil {
		return nil, err
	}
	if f.home != "" && canonical != "tool4d" && os.PathSeparator == '\\' {
		return nil, &version.UsageError{Msg: "4D and 4D Server on Windows ignore LOCALAPPDATA/APPDATA overrides, so --home cannot isolate them; run the self-hosted runner as a dedicated user instead"}
	}
	if f.project == "" {
		return nil, &version.UsageError{Msg: "no project given"}
	}
	if f.runtimeVersion == "" && f.runtimePath != "" {
		v, err := detectVersion(f.runtimePath)
		if err != nil {
			return nil, err
		}
		f.runtimeVersion = v
	}
	ghToken, glToken, err := f.tokens()
	if err != nil {
		return nil, &version.UsageError{Msg: err.Error()}
	}
	gh := provider.NewGitHub(ghToken, f.offline)
	gh.H.Timeout = f.timeout
	if testTransport != nil {
		gh.H.Client = &http.Client{Transport: testTransport}
		gh.H.Sleep = func(time.Duration) {}
	}
	if f.apiURL != "" {
		u, err := url.Parse(f.apiURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
			return nil, &version.UsageError{Msg: "--github-api-url must be an https URL (or http on a loopback host)"}
		}
		if err := gh.SetAPI(strings.TrimRight(f.apiURL, "/")); err != nil {
			return nil, &version.UsageError{Msg: err.Error()}
		}
	}
	// 4D's gitlab.json holds one token and no host: it is sent to gitlab.com only. Whether 4D also sends it to a
	// self-hosted host has not been observed, so it is not.
	factory := func(host string) provider.Client {
		token := ""
		if host == provider.GitLabCom {
			token = glToken
		}
		g := provider.NewGitLab(host, token, f.offline)
		g.H.Timeout = f.timeout
		if testTransport != nil {
			g.H.Client = &http.Client{Transport: testTransport}
			g.H.Sleep = func(time.Duration) {}
		}
		return g
	}
	return resolver.New(resolver.Options{
		Project: f.project, Runtime: canonical, RuntimeVersion: f.runtimeVersion, Home: f.home,
		DependenciesFolder: f.depsFolder, ArchiveStore: f.archiveStore, Update: f.update, NoWriteLock: f.noWriteLock,
		User: f.user, Rewrite: f.rewrite, PlanOnly: planOnly,
	}, gh, factory, glToken != "")
}

func errorExit(stderr writer, err error) int {
	if _, ok := err.(*version.UsageError); ok {
		fmt.Fprintf(stderr, "4d-components: %s\n", err)
		return exitUsage
	}
	fmt.Fprintf(stderr, "4d-components: %s\n", err)
	return exitInternal
}

func emit(stdout writer, path string, v any) error {
	data, err := ojson.Marshal(v, "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if path == "" {
		_, err = stdout.Write(data)
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func discard() io.Writer { return io.Discard }
