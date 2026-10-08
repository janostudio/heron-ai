package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/heron-ai/heron-engine/pkg/types"
)

var (
	ErrPathOutsideWorkspace = errors.New("path escapes workspace")
	ErrRevisionConflict     = errors.New("workspace revision conflict")
	ErrFileExists           = errors.New("file already exists")
	ErrFileNotFound         = errors.New("file not found")
	ErrEditTargetNotFound   = errors.New("edit target not found")
	ErrEditTargetAmbiguous  = errors.New("edit target is ambiguous")
)

const (
	DefaultReadMaxBytes     = 256 * 1024
	DefaultSearchMaxBytes   = 1024 * 1024
	DefaultSearchMaxChars   = 64 * 1024
	DefaultSearchMaxResults = 500
	DefaultCommandMaxBytes  = 64 * 1024
	DefaultGlobMaxResults   = 10_000
)

// localWorkspace is the concrete local-filesystem backend. It implements the
// Workspace interface. The private resolve / ensureResolvedWithinWorkspace
// helpers stay on this type (not on the interface) since only the local
// backend needs them.
type localWorkspace struct {
	root string
}

func NewLocal(root string) (Workspace, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("workspace root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return nil, fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory")
	}
	return &localWorkspace{root: filepath.Clean(absolute)}, nil
}

func (s *localWorkspace) Root() string {
	return s.root
}

func (s *localWorkspace) IsRemote() bool { return false }

// ResolvePathForTool validates and returns the workspace-relative path for
// optional Tools that need to pass a path to an external helper.
func (s *localWorkspace) ResolvePathForTool(path string) (string, string, error) {
	return s.resolve(path)
}

// ResolvePathForToolRestricted is ResolvePathForTool with the caller's path
// restriction applied.
//
// It exists because resolving a path and being allowed to use it are two
// different questions, and a helper that answers only the first hands a
// restricted path straight to an external command. CodeNav does exactly that:
// it resolves the file and then passes the workspace root to a language server
// that will happily read anything under it.
//
// The path is normalized the same way the read paths normalize theirs, so a
// restriction cannot be defeated by spelling.
func (s *localWorkspace) ResolvePathForToolRestricted(path string, restrict Restriction) (string, string, error) {
	full, relative, err := s.resolve(path)
	if err != nil {
		return "", "", err
	}
	if !restrict.permits(relative) {
		// Same shape as Read: report absence rather than refusal, so the
		// caller cannot use this to probe which paths exist.
		return "", "", fmt.Errorf("%w: %s", ErrFileNotFound, relative)
	}
	return full, relative, nil
}

// NewOperationID returns a unique operation identifier for external Tools
// that publish WorkspaceOperation audit facts.
func NewOperationID() string {
	return newOperationID()
}

type ReadRequest struct {
	TurnID    string
	Path      string
	LineStart int
	LineEnd   int
	MaxBytes  int
	// Restrict is the caller's path restriction. See restrict.go: the walk
	// checks it per returned path, and Read checks the resolved path before
	// opening the file.
	Restrict Restriction
}

type ReadResult struct {
	Content    string
	Revision   string
	Truncated  bool
	LineStart  int
	LineEnd    int
	TotalLines int
	TotalBytes int
	Operation  types.WorkspaceOperation
}

func (s *localWorkspace) Read(ctx context.Context, req ReadRequest) (ReadResult, error) {
	start := time.Now().UTC()
	fullPath, relative, err := s.resolve(req.Path)
	if err != nil {
		return ReadResult{}, err
	}
	// The denial is checked on the *resolved* relative path, after symlink
	// resolution, not on req.Path. A request naming "link/private.md" where
	// "link" points into a denied directory must be denied too, and only the
	// resolved form can see that. Reading the file first and filtering the
	// result would already have opened it.
	if !req.Restrict.permits(relative) {
		// ErrFileNotFound, not a distinct "denied" error: a separate error
		// would confirm that the file exists and is off limits, which is
		// exactly the fact the restriction exists to withhold. The read
		// reports what a caller without access can see.
		return ReadResult{}, fmt.Errorf("%w: %s", ErrFileNotFound, relative)
	}
	data, err := os.ReadFile(fullPath)
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
	content, lineStart, lineEnd, truncated, err := selectReadContent(
		string(data),
		req.LineStart,
		req.LineEnd,
		maxBytes,
	)
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
	}, contextErr(ctx)
}

type WriteRequest struct {
	TurnID       string
	Path         string
	Content      string
	BaseRevision string
	Mode         string
	OldText      string
	NewText      string
	// Restrict is the caller's path restriction. See restrict.go. It is
	// checked on the resolved path before anything is read or written, and
	// it covers every mode — see localWorkspace.Write for why the check
	// cannot be attached to one mode without leaving the others open.
	Restrict Restriction
}

type WriteResult struct {
	Revision     string
	Mode         string
	ChangedBytes int
	MatchedCount int
	Operation    types.WorkspaceOperation
}

func (s *localWorkspace) Write(ctx context.Context, req WriteRequest) (WriteResult, error) {
	start := time.Now().UTC()
	fullPath, relative, err := s.resolve(req.Path)
	if err != nil {
		return WriteResult{}, err
	}
	// Same resolved-path check as Read, same position: before the file is
	// touched. It deliberately runs before the mode switch, so create,
	// replace and edit are all covered by this one call — a check placed
	// inside one branch is a check the other branches do not have.
	//
	// # Why a denied write is refused loudly, not silently skipped
	//
	// The read side reports denial as absence, so that a caller cannot use
	// the error to learn that a file exists. That reasoning does not carry
	// over to writes, because the two failures look different to the one
	// party who matters — the model. A denied read returns nothing, which
	// reads as "no results" and prompts another attempt. A denied write that
	// returned success would report a file as changed when nothing on disk
	// moved, and the model would build its next steps on a file that does
	// not exist. There is also nothing left to hide here: the caller already
	// named the path, so refusing it discloses nothing it did not supply.
	//
	// ErrFileNotFound is reused rather than a new sentinel, so a caller that
	// branches on "no such file" keeps working without a new case, and so
	// the read and write sides of the restriction stay one concept.
	if !req.Restrict.permits(relative) {
		return WriteResult{}, fmt.Errorf("%w: %s", ErrFileNotFound, relative)
	}
	if err := contextErr(ctx); err != nil {
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

	current, readErr := os.ReadFile(fullPath)
	exists := readErr == nil
	if readErr != nil && !os.IsNotExist(readErr) {
		return WriteResult{}, readErr
	}
	fileMode := os.FileMode(0644)
	if exists {
		if info, statErr := os.Stat(fullPath); statErr == nil {
			fileMode = info.Mode().Perm()
		} else {
			return WriteResult{}, statErr
		}
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
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return WriteResult{}, err
	}
	if err := atomicWriteFile(fullPath, []byte(req.Content), fileMode); err != nil {
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

type CommandRequest struct {
	TurnID         string
	Command        string
	Args           []string
	Env            []string
	Stdin          string
	Shell          string
	Kind           string
	MaxOutputBytes int
}

type CommandResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	TimedOut  bool
	Canceled  bool
	Truncated bool
	Operation types.WorkspaceOperation
}

func (s *localWorkspace) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	start := time.Now().UTC()
	if strings.TrimSpace(req.Command) == "" {
		return CommandResult{}, errors.New("command is required")
	}

	var cmd *exec.Cmd
	if len(req.Args) == 0 {
		shell := req.Shell
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd = exec.CommandContext(ctx, shell, "-c", req.Command)
	} else {
		cmd = exec.CommandContext(ctx, req.Command, req.Args...)
	}
	cmd.Dir = s.root
	if len(req.Env) > 0 {
		cmd.Env = req.Env
	}
	cmd.Stdin = strings.NewReader(req.Stdin)
	var stdout, stderr limitedBuffer
	outputLimit := req.MaxOutputBytes
	if outputLimit <= 0 {
		outputLimit = DefaultCommandMaxBytes
	}
	stdout.limit = outputLimit
	stderr.limit = outputLimit
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	kind := req.Kind
	if kind == "" {
		kind = "test"
	}
	timedOut := ctx != nil && ctx.Err() == context.DeadlineExceeded
	canceled := ctx != nil && ctx.Err() == context.Canceled
	result := CommandResult{
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
	}
	return result, err
}

func (s *localWorkspace) Glob(pattern string) ([]string, error) {
	return s.GlobWithOptions(context.Background(), GlobRequest{Pattern: pattern})
}

type GlobRequest struct {
	TurnID      string
	Pattern     string
	MaxResults  int
	IncludeDirs bool
	// Restrict is the caller's path restriction. See restrict.go.
	Restrict Restriction
}

func (s *localWorkspace) GlobWithOptions(ctx context.Context, req GlobRequest) ([]string, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	pattern := strings.TrimSpace(req.Pattern)
	if strings.TrimSpace(pattern) == "" {
		return nil, errors.New("glob pattern is required")
	}
	if filepath.IsAbs(pattern) {
		return nil, fmt.Errorf("%w: %s", ErrPathOutsideWorkspace, pattern)
	}
	matcher, err := compileGlob(pattern)
	if err != nil {
		return nil, err
	}
	var matches []string
	maxResults := req.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultGlobMaxResults
	}
	err = filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		relative, relErr := filepath.Rel(s.root, path)
		if relErr != nil {
			return relErr
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if entry.IsDir() && isDefaultExcludedDir(relative) {
			return filepath.SkipDir
		}
		// Prune a denied directory instead of merely dropping its matches:
		// walking into it would still read every entry's name, and a walk
		// that visits a tree it will never return cannot be distinguished
		// from one that never saw it — the cheap correct thing is to not
		// descend at all. permits covers files too, so this one check
		// handles both; a directory that is allowed back (a caller's own
		// private tree, see Restriction) is not pruned.
		if !req.Restrict.permits(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() && !req.IncludeDirs {
			return nil
		}
		if matcher.MatchString(relative) {
			matches = append(matches, relative)
			if len(matches) >= maxResults {
				return filepath.SkipAll
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return nil, err
	}
	sort.Strings(matches)
	return matches, nil
}

type SearchRequest struct {
	TurnID       string
	Pattern      string
	Path         string
	Include      string
	Regex        bool
	IgnoreCase   bool
	MaxResults   int
	MaxChars     int
	MaxFileBytes int
	// Restrict is the caller's path restriction. See restrict.go. It applies
	// to every file the walk would return, not only to Path: searching from
	// the workspace root must not surface a restricted file further down the
	// tree.
	Restrict Restriction
}

type SearchMatch struct {
	Path    string
	Line    int
	Content string
}

type SearchResult struct {
	Matches   []SearchMatch
	Truncated bool
	Operation types.WorkspaceOperation
}

func (s *localWorkspace) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if strings.TrimSpace(req.Pattern) == "" {
		return SearchResult{}, errors.New("search pattern is required")
	}
	rootPath := req.Path
	if rootPath == "" {
		rootPath = "."
	}
	fullPath, relativeRoot, err := s.resolve(rootPath)
	if err != nil {
		return SearchResult{}, err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return SearchResult{}, err
	}
	var matcher *regexp.Regexp
	pattern := req.Pattern
	if req.IgnoreCase {
		pattern = "(?i)" + pattern
	}
	if req.Regex {
		matcher, err = regexp.Compile(pattern)
		if err != nil {
			return SearchResult{}, fmt.Errorf("invalid search regex: %w", err)
		}
	}
	start := time.Now().UTC()
	result := SearchResult{}
	usedChars := 0
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
	visit := func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := contextErr(ctx); err != nil {
			return err
		}
		relative, err := filepath.Rel(s.root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if isDefaultExcludedDir(relative) {
				return filepath.SkipDir
			}
			// Prune a denied directory so its files are never opened. The
			// same check covers files below, but a directory prefix denial
			// has to be caught here — otherwise every file inside it is
			// read from disk before being discarded, which is both wasteful
			// and one refactor away from returning what it just read.
			if !req.Restrict.permits(relative) {
				return filepath.SkipDir
			}
			return nil
		}
		if !req.Restrict.permits(relative) {
			return nil
		}
		if req.Include != "" {
			matched, matchErr := filepath.Match(req.Include, filepath.Base(relative))
			if matchErr != nil {
				return matchErr
			}
			if !matched {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > int64(maxFileBytes) {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.IndexByte(data, 0) >= 0 {
			return nil
		}
		for lineNo, line := range splitLinesForWorkspace(string(data)) {
			matched := false
			if matcher != nil {
				matched = matcher.MatchString(line)
			} else if req.IgnoreCase {
				matched = strings.Contains(strings.ToLower(line), strings.ToLower(req.Pattern))
			} else {
				matched = strings.Contains(line, req.Pattern)
			}
			if !matched {
				continue
			}
			formattedSize := len(relative) + len(line) + 32
			if usedChars+formattedSize > maxChars {
				result.Truncated = true
				return filepath.SkipAll
			}
			result.Matches = append(result.Matches, SearchMatch{Path: relative, Line: lineNo + 1, Content: line})
			usedChars += formattedSize
			if len(result.Matches) >= maxResults {
				result.Truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	}
	if info.IsDir() {
		err = filepath.WalkDir(fullPath, visit)
	} else {
		err = visit(fullPath, dirEntryInfo{info}, nil)
	}
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return SearchResult{}, err
	}
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

func (s *localWorkspace) resolve(path string) (string, string, error) {
	if strings.TrimSpace(path) == "" {
		return "", "", errors.New("workspace path is required")
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.root, candidate)
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return "", "", err
	}
	clean := filepath.Clean(absolute)
	relative, err := filepath.Rel(s.root, clean)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("%w: %s", ErrPathOutsideWorkspace, path)
	}
	if err := s.ensureResolvedWithinWorkspace(clean); err != nil {
		return "", "", err
	}
	return clean, filepath.ToSlash(relative), nil
}

func (s *localWorkspace) ensureResolvedWithinWorkspace(path string) error {
	root, err := filepath.EvalSymlinks(s.root)
	if err != nil {
		return err
	}
	candidate := path
	for {
		if _, statErr := os.Lstat(candidate); statErr == nil {
			break
		} else if !os.IsNotExist(statErr) {
			return statErr
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return fmt.Errorf("%w: %s", ErrPathOutsideWorkspace, path)
		}
		candidate = parent
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, filepath.Clean(resolved))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s", ErrPathOutsideWorkspace, path)
	}
	return nil
}

func revisionOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func truncateText(text string, maxBytes int) string {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	text = text[:maxBytes]
	for len(text) > 0 && !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text + "…"
}

func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".heron-write-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempName, path)
}

func countLines(text string) int {
	if text == "" {
		return 0
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		return len(lines) - 1
	}
	return len(lines)
}

func splitLinesForWorkspace(text string) []string {
	if text == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}

func selectReadContent(text string, start, end, maxBytes int) (string, int, int, bool, error) {
	lines := splitLinesForWorkspace(text)
	if len(lines) == 0 {
		return "", 1, 0, false, nil
	}
	if start <= 0 {
		start = 1
	}
	if end <= 0 || end > len(lines) {
		end = len(lines)
	}
	if start > end || start > len(lines) {
		return "", 0, 0, false, fmt.Errorf("invalid line range %d-%d", start, end)
	}
	content := ""
	if start == 1 && end == len(lines) {
		content = text
	} else {
		content = strings.Join(lines[start-1:end], "\n")
		if end < len(lines) || strings.HasSuffix(text, "\n") {
			content += "\n"
		}
	}
	truncated := false
	if maxBytes > 0 && len(content) > maxBytes {
		content = content[:maxBytes]
		for len(content) > 0 && !utf8.ValidString(content) {
			content = content[:len(content)-1]
		}
		truncated = true
	}
	return content, start, end, truncated, nil
}

func isDefaultExcludedDir(relative string) bool {
	base := filepath.Base(filepath.Clean(relative))
	switch base {
	case ".git", "node_modules", "dist", "build", "target":
		return true
	default:
		return false
	}
}

func compileGlob(pattern string) (*regexp.Regexp, error) {
	pattern = filepath.ToSlash(pattern)
	if _, err := filepath.Match(pattern, ""); err != nil {
		return nil, fmt.Errorf("invalid glob pattern: %w", err)
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '^', '$', '|', '{', '}', '\\':
			b.WriteByte('\\')
			b.WriteByte(pattern[i])
		default:
			b.WriteByte(pattern[i])
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return b.buffer.Write(p)
	}
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		b.truncated = true
		_, err := b.buffer.Write(p[:remaining])
		return len(p), err
	}
	return b.buffer.Write(p)
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

type dirEntryInfo struct{ os.FileInfo }

func (d dirEntryInfo) Type() os.FileMode          { return d.Mode().Type() }
func (d dirEntryInfo) Info() (os.FileInfo, error) { return d.FileInfo, nil }

func newOperationID() string {
	return fmt.Sprintf("wo_%d", time.Now().UnixNano())
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
