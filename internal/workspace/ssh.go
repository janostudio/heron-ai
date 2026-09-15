package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/heron-ai/heron-engine/pkg/types"
)

const defaultRemoteRoot = "/root/workspace"

// sshWorkspace is the remote SSH execution backend. It implements the
// Workspace interface using an SSH connection: Read/Write/Glob go through SFTP,
// Run/Search go through SSH exec, and all results still produce a
// WorkspaceOperation audit fact (path + revision) so the session.jsonl audit
// chain is not broken by remoteness.
type sshWorkspace struct {
	root   string
	client *ssh.Client
}

// newSSHWorkspace establishes an SSH connection (with an optional SFTP
// subservice) and returns a Workspace backed by the remote host. The SFTP
// subsystem is opened lazily per file operation because a single SFTP session
// is not safe for concurrent use; the underlying SSH transport is.
func newSSHWorkspace(cfg types.SSHConfig) (Workspace, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, errors.New("ssh host is required")
	}
	port := cfg.Port
	if port == 0 {
		port = 22
	}
	user := cfg.User
	if user == "" {
		user = "root"
	}
	root := cfg.Root
	if strings.TrimSpace(root) == "" {
		root = defaultRemoteRoot
	}
	root = path.Clean("/" + strings.TrimLeft(root, "/"))

	authMethods, err := sshAuthMethods(cfg)
	if err != nil {
		return nil, err
	}

	clientConfig := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // no known_hosts management in this iteration
		Timeout:         15 * time.Second,
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, port)
	client, err := ssh.Dial("tcp", addr, clientConfig)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	return &sshWorkspace{root: root, client: client}, nil
}

// sshAuthMethods builds the SSH authentication method list from the config.
// A private key (key_path) takes precedence; otherwise password is used.
// If neither is set and no ssh-agent is reachable, an empty method list is
// returned and the connection will fail with a clear auth error.
func sshAuthMethods(cfg types.SSHConfig) ([]ssh.AuthMethod, error) {
	if strings.TrimSpace(cfg.KeyPath) != "" {
		expanded := expandHome(cfg.KeyPath)
		key, err := os.ReadFile(expanded)
		if err != nil {
			return nil, fmt.Errorf("read ssh key %s: %w", expanded, err)
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("parse ssh key %s: %w", expanded, err)
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	}
	if cfg.Password != "" {
		return []ssh.AuthMethod{ssh.Password(cfg.Password)}, nil
	}
	if agentConn, err := netSSHAgent(); err == nil {
		return []ssh.AuthMethod{ssh.PublicKeysCallback(agentConn.Signers)}, nil
	}
	return nil, errors.New("ssh auth: no key_path, password, or ssh-agent available")
}

func (s *sshWorkspace) Root() string {
	return s.root
}

func (s *sshWorkspace) IsRemote() bool { return true }

// ResolvePathForTool validates and normalizes a workspace path. Because the
// remote filesystem has no meaningful local-symlink semantics we can rely on
// (we do not resolve symlinks remotely), this is a pure lexical check: path.Clean
// plus a prefix check to prevent "../" traversal outside the workspace root.
func (s *sshWorkspace) ResolvePathForTool(p string) (string, string, error) {
	return s.resolve(p)
}

// resolve returns (absolute remote path, workspace-relative path, error).
// Relative paths are joined against the remote root; absolute paths must still
// fall inside root. Traversal outside the root is rejected.
func (s *sshWorkspace) resolve(p string) (string, string, error) {
	if strings.TrimSpace(p) == "" {
		return "", "", errors.New("workspace path is required")
	}
	clean := path.Clean(p)
	if !path.IsAbs(clean) {
		clean = path.Join(s.root, clean)
	}
	relative, err := relPath(s.root, clean)
	if err != nil || relative == ".." || strings.HasPrefix(relative, "../") {
		return "", "", fmt.Errorf("%w: %s", ErrPathOutsideWorkspace, p)
	}
	return clean, relative, nil
}

// sftpSession opens a fresh SFTP client session. Callers must close it.
func (s *sshWorkspace) sftpSession() (*sftp.Client, error) {
	return sftp.NewClient(s.client)
}

func (s *sshWorkspace) Read(ctx context.Context, req ReadRequest) (ReadResult, error) {
	if err := contextErr(ctx); err != nil {
		return ReadResult{}, err
	}
	start := time.Now().UTC()
	abs, relative, err := s.resolve(req.Path)
	if err != nil {
		return ReadResult{}, err
	}
	client, err := s.sftpSession()
	if err != nil {
		return ReadResult{}, fmt.Errorf("open sftp session: %w", err)
	}
	defer client.Close()
	data, err := sftpReadAll(client, abs)
	if err != nil {
		if os.IsNotExist(err) {
			return ReadResult{}, fmt.Errorf("%w: %s", ErrFileNotFound, relative)
		}
		return ReadResult{}, err
	}
	revision := revisionOf(data)
	maxBytes := req.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultReadMaxBytes
	}
	content, lineStart, lineEnd, truncated, err := selectReadContent(string(data), req.LineStart, req.LineEnd, maxBytes)
	if err != nil {
		return ReadResult{}, err
	}
	return ReadResult{
		Content:    content,
		Revision:   revision,
		Truncated:  truncated,
		LineStart:  lineStart,
		LineEnd:    lineEnd,
		TotalLines: countLines(string(data)),
		TotalBytes: len(data),
		Operation: types.WorkspaceOperation{
			OperationID: newOperationID(),
			TurnID:      req.TurnID,
			Kind:        "read",
			Path:        relative,
			Revision:    revision,
			Lines:       []int{lineStart, lineEnd},
			Excerpt:     content,
			Truncated:   truncated,
			Summary:     fmt.Sprintf("read %s", relative),
			StartedAt:   start,
			FinishedAt:  time.Now().UTC(),
		},
	}, nil
}

func (s *sshWorkspace) Write(ctx context.Context, req WriteRequest) (WriteResult, error) {
	if err := contextErr(ctx); err != nil {
		return WriteResult{}, err
	}
	start := time.Now().UTC()
	abs, relative, err := s.resolve(req.Path)
	if err != nil {
		return WriteResult{}, err
	}

	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "replace"
	}
	switch mode {
	case "create", "replace", "edit":
	default:
		return WriteResult{}, fmt.Errorf("unsupported write mode %q", mode)
	}

	client, err := s.sftpSession()
	if err != nil {
		return WriteResult{}, fmt.Errorf("open sftp session: %w", err)
	}
	defer client.Close()

	current, readErr := sftpReadAll(client, abs)
	exists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return WriteResult{}, readErr
	}
	currentRevision := revisionOf(current)
	if mode == "create" && exists {
		return WriteResult{}, fmt.Errorf("%w: %s", ErrFileExists, relative)
	}
	if mode == "edit" {
		if !exists {
			return WriteResult{}, fmt.Errorf("%w: %s", ErrFileNotFound, relative)
		}
		if req.OldText == "" {
			return WriteResult{}, errors.New("old_text is required for edit mode")
		}
		if req.BaseRevision == "" {
			return WriteResult{}, errors.New("base_revision is required for edit mode")
		}
		if req.BaseRevision != currentRevision {
			return WriteResult{}, fmt.Errorf("%w: %s", ErrRevisionConflict, relative)
		}
		matched := strings.Count(string(current), req.OldText)
		if matched == 0 {
			return WriteResult{}, fmt.Errorf("%w: %s", ErrEditTargetNotFound, relative)
		}
		if matched != 1 {
			return WriteResult{}, fmt.Errorf("%w: %s (matched %d times)", ErrEditTargetAmbiguous, relative, matched)
		}
		req.Content = strings.Replace(string(current), req.OldText, req.NewText, 1)
	} else if req.BaseRevision != "" && req.BaseRevision != currentRevision {
		return WriteResult{}, fmt.Errorf("%w: %s", ErrRevisionConflict, relative)
	}

	if err := client.MkdirAll(path.Dir(abs)); err != nil {
		return WriteResult{}, err
	}
	if err := sftpWriteAll(client, abs, []byte(req.Content)); err != nil {
		return WriteResult{}, err
	}

	revision := revisionOf([]byte(req.Content))
	return WriteResult{
		Revision:     revision,
		Mode:         mode,
		ChangedBytes: len(req.Content),
		MatchedCount: func() int {
			if mode == "edit" {
				return 1
			}
			return 0
		}(),
		Operation: types.WorkspaceOperation{
			OperationID:  newOperationID(),
			TurnID:       req.TurnID,
			Kind:         "write",
			Path:         relative,
			Revision:     revision,
			BaseRevision: req.BaseRevision,
			Excerpt:      truncateText(req.Content, 4096),
			Summary:      fmt.Sprintf("write %s (%s)", relative, mode),
			StartedAt:    start,
			FinishedAt:   time.Now().UTC(),
		},
	}, nil
}

func (s *sshWorkspace) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now().UTC()
	if strings.TrimSpace(req.Command) == "" {
		return CommandResult{}, errors.New("command is required")
	}

	session, err := s.client.NewSession()
	if err != nil {
		return CommandResult{}, fmt.Errorf("new ssh session: %w", err)
	}
	defer session.Close()

	shell := req.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	command := req.Command
	if len(req.Args) > 0 {
		command = strings.Join(append([]string{req.Command}, req.Args...), " ")
	}
	if len(req.Env) > 0 {
		for _, e := range req.Env {
			if i := strings.IndexByte(e, '='); i >= 0 {
				_ = session.Setenv(e[:i], e[i+1:])
			}
		}
	}
	if req.Stdin != "" {
		session.Stdin = strings.NewReader(req.Stdin)
	}

	var stdout, stderr limitedBuffer
	outputLimit := req.MaxOutputBytes
	if outputLimit <= 0 {
		outputLimit = DefaultCommandMaxBytes
	}
	stdout.limit = outputLimit
	stderr.limit = outputLimit
	session.Stdout = &stdout
	session.Stderr = &stderr

	// The remote command is executed with the workspace root as cwd so that
	// relative paths resolve exactly as they would for the local backend.
	runCmd := fmt.Sprintf("cd %s && %s -c %s", shellQuote(s.root), shellQuote(shell), shellQuote(command))

	// A goroutine that closes the session when ctx is done, which causes the
	// blocking Run() to return; exit code is then inferred from the context.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-done:
		}
	}()

	runErr := session.Run(runCmd)

	timedOut := ctx.Err() == context.DeadlineExceeded
	canceled := ctx.Err() == context.Canceled

	exitCode := 0
	if runErr != nil {
		exitCode = 1
		if ee, ok := runErr.(*ssh.ExitError); ok {
			exitCode = ee.ExitStatus()
		}
	}
	kind := req.Kind
	if kind == "" {
		kind = "test"
	}
	return CommandResult{
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		ExitCode:  exitCode,
		TimedOut:  timedOut,
		Canceled:  canceled,
		Truncated: stdout.truncated || stderr.truncated,
		Operation: types.WorkspaceOperation{
			OperationID: newOperationID(),
			TurnID:      req.TurnID,
			Kind:        kind,
			Command:     req.Command,
			ExitCode:    exitCode,
			Truncated:   stdout.truncated || stderr.truncated,
			Summary:     fmt.Sprintf("run %s %s", kind, req.Command),
			StartedAt:   start,
			FinishedAt:  time.Now().UTC(),
		},
	}, runErr
}

func (s *sshWorkspace) Glob(pattern string) ([]string, error) {
	return s.GlobWithOptions(context.Background(), GlobRequest{Pattern: pattern})
}

func (s *sshWorkspace) GlobWithOptions(ctx context.Context, req GlobRequest) ([]string, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	pattern := strings.TrimSpace(req.Pattern)
	if pattern == "" {
		return nil, errors.New("glob pattern is required")
	}
	if path.IsAbs(pattern) {
		return nil, fmt.Errorf("%w: %s", ErrPathOutsideWorkspace, pattern)
	}
	matcher, err := compileGlob(pattern)
	if err != nil {
		return nil, err
	}
	client, err := s.sftpSession()
	if err != nil {
		return nil, fmt.Errorf("open sftp session: %w", err)
	}
	defer client.Close()

	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultGlobMaxResults
	}
	var matches []string
	var walk func(dir string) error
	walk = func(dir string) error {
		if err := contextErr(ctx); err != nil {
			return err
		}
		entries, err := client.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			rel := path.Join(dir, entry.Name())
			relative, err := relPath(s.root, rel)
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if isDefaultExcludedDir(relative) {
					continue
				}
				if req.IncludeDirs && matcher.MatchString(relative) {
					matches = append(matches, relative)
					if len(matches) >= maxResults {
						return errGlobLimit
					}
				}
				if err := walk(rel); err != nil {
					return err
				}
				continue
			}
			if matcher.MatchString(relative) {
				matches = append(matches, relative)
				if len(matches) >= maxResults {
					return errGlobLimit
				}
			}
		}
		return nil
	}
	err = walk(s.root)
	if err != nil && !errors.Is(err, errGlobLimit) {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

// errGlobLimit is an internal sentinel used to stop a glob walk once the
// result limit is reached. It must not escape the package.
var errGlobLimit = errors.New("glob result limit reached")

func (s *sshWorkspace) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if strings.TrimSpace(req.Pattern) == "" {
		return SearchResult{}, errors.New("search pattern is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	rootPath := req.Path
	if rootPath == "" {
		rootPath = "."
	}
	abs, relativeRoot, err := s.resolve(rootPath)
	if err != nil {
		return SearchResult{}, err
	}
	start := time.Now().UTC()

	// abs is used only to confirm resolution succeeded; the actual search is
	// performed by the remote grep with the workspace root as cwd.
	_ = abs

	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultSearchMaxResults
	}
	maxChars := req.MaxChars
	if maxChars <= 0 {
		maxChars = DefaultSearchMaxChars
	}
	maxFileBytes := req.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = DefaultSearchMaxBytes
	}

	// Pre-validate a regex so a bad pattern fails fast with a clear error
	// (mirroring the local backend) rather than surfacing a remote grep error.
	if req.Regex {
		re, err := regexp.Compile(req.Pattern)
		if err != nil {
			return SearchResult{}, fmt.Errorf("invalid search regex: %w", err)
		}
		_ = re
	}

	session, err := s.client.NewSession()
	if err != nil {
		return SearchResult{}, fmt.Errorf("new ssh session: %w", err)
	}
	defer session.Close()

	var out limitedBuffer
	out.limit = maxChars
	session.Stdout = &out
	session.Stderr = io.Discard

	// Build a portable grep invocation. -n prints line numbers, -I skips
	// binary files, -r recurses, -s suppresses "not found" noise. -m caps
	// matches per file; the result set is capped globally afterward.
	grepArgs := []string{"-n", "-I", "-r", "-s"}
	if req.Regex {
		grepArgs = append(grepArgs, "-E")
	} else {
		grepArgs = append(grepArgs, "-F")
	}
	if req.IgnoreCase {
		grepArgs = append(grepArgs, "-i")
	}
	if req.Include != "" {
		grepArgs = append(grepArgs, "--include", req.Include)
	}
	if maxFileBytes > 0 {
		grepArgs = append(grepArgs, fmt.Sprintf("--max-filesize=%d", maxFileBytes))
	}
	grepArgs = append(grepArgs, "--", req.Pattern, ".")

	args := make([]string, 0, len(grepArgs)+2)
	args = append(args, "grep")
	args = append(args, grepArgs...)
	grepCmd := fmt.Sprintf("cd %s && %s", shellQuote(s.root), strings.Join(args, " "))

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = session.Close()
		case <-done:
		}
	}()

	_ = session.Run(grepCmd)
	raw := out.String()

	result := SearchResult{}
	usedChars := 0
	for _, line := range strings.Split(raw, "\n") {
		if line == "" {
			continue
		}
		if len(result.Matches) >= maxResults || usedChars+len(line)+32 > maxChars {
			result.Truncated = true
			break
		}
		rel, lineNo, content, ok := parseGrepLine(line)
		if !ok {
			continue
		}
		result.Matches = append(result.Matches, SearchMatch{Path: rel, Line: lineNo, Content: content})
		usedChars += len(rel) + len(content) + 32
	}
	sort.Slice(result.Matches, func(i, j int) bool {
		if result.Matches[i].Path != result.Matches[j].Path {
			return result.Matches[i].Path < result.Matches[j].Path
		}
		return result.Matches[i].Line < result.Matches[j].Line
	})

	result.Operation = types.WorkspaceOperation{
		OperationID: newOperationID(),
		TurnID:      req.TurnID,
		Kind:        "search",
		Path:        relativeRoot,
		Summary:     fmt.Sprintf("search %s in %s", req.Pattern, relativeRoot),
		Truncated:   result.Truncated,
		StartedAt:   start,
		FinishedAt:  time.Now().UTC(),
	}
	return result, nil
}

// parseGrepLine parses a single `grep -n` output line of the form
// "path:lineno:content" into its components. A missing/blank path (e.g. the
// "Binary file ... matches" case) yields ok=false and is skipped.
func parseGrepLine(line string) (rel string, lineNo int, content string, ok bool) {
	idx := strings.IndexByte(line, ':')
	if idx < 0 {
		return "", 0, "", false
	}
	rel = line[:idx]
	rest := line[idx+1:]
	idx2 := strings.IndexByte(rest, ':')
	if idx2 < 0 {
		return "", 0, "", false
	}
	lineNoStr := rest[:idx2]
	content = rest[idx2+1:]
	n, err := parseInt(lineNoStr)
	if err != nil {
		return "", 0, "", false
	}
	if rel == "" {
		return "", 0, "", false
	}
	return rel, n, content, true
}

func parseInt(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, errors.New("empty")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("non-digit")
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// sftpReadAll reads the full contents of a remote file, translating the SFTP
// "file does not exist" error into os.ErrNotExist for callers that rely on
// os.IsNotExist.
func sftpReadAll(client *sftp.Client, name string) ([]byte, error) {
	f, err := client.Open(name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, os.ErrNotExist
		}
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func sftpWriteAll(client *sftp.Client, name string, data []byte) error {
	f, err := client.Create(name)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, bytes.NewReader(data))
	return err
}

// expandHome expands a leading "~" to the current user's home directory.
func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if p == "~" {
				return home
			}
			return path.Join(home, p[2:])
		}
	}
	return p
}

// shellQuote wraps a string in single quotes for use inside a POSIX shell
// command. Embedded single quotes are escaped with the "'\''" idiom.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// relPath returns the relative path from base to target using POSIX path
// semantics. Both arguments must be clean absolute paths with target inside
// base. This mirrors filepath.Rel for the remote backend where the `path`
// package (unlike filepath) has no Rel helper.
func relPath(base, target string) (string, error) {
	if base == target {
		return ".", nil
	}
	if base == "/" {
		return strings.TrimPrefix(target, "/"), nil
	}
	if !strings.HasPrefix(target, base+"/") {
		return "", fmt.Errorf("target %q is not under base %q", target, base)
	}
	return strings.TrimPrefix(target, base+"/"), nil
}

// netSSHAgent connects to the SSH agent at SSH_AUTH_SOCK and returns an
// agent.Agent. It returns an error when no agent is available.
func netSSHAgent() (agent.Agent, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK not set")
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return nil, err
	}
	return agent.NewClient(conn), nil
}
