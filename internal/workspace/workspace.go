package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/heron-ai/heron-engine/pkg/types"
)

var (
	ErrPathOutsideWorkspace = errors.New("path escapes workspace")
	ErrRevisionConflict     = errors.New("workspace revision conflict")
)

type Service struct {
	root string
}

func New(root string) (*Service, error) {
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
	return &Service{root: filepath.Clean(absolute)}, nil
}

func (s *Service) Root() string {
	return s.root
}

type ReadRequest struct {
	TurnID string
	Path   string
}

type ReadResult struct {
	Content   string
	Revision  string
	Operation types.WorkspaceOperation
}

func (s *Service) Read(ctx context.Context, req ReadRequest) (ReadResult, error) {
	start := time.Now().UTC()
	fullPath, relative, err := s.resolve(req.Path)
	if err != nil {
		return ReadResult{}, err
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return ReadResult{}, err
	}
	revision := revisionOf(data)
	return ReadResult{
		Content:  string(data),
		Revision: revision,
		Operation: types.WorkspaceOperation{
			OperationID: newOperationID(),
			TurnID:      req.TurnID,
			Kind:        "read",
			Path:        relative,
			Revision:    revision,
			Excerpt:     string(data),
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
}

type WriteResult struct {
	Revision  string
	Operation types.WorkspaceOperation
}

func (s *Service) Write(ctx context.Context, req WriteRequest) (WriteResult, error) {
	start := time.Now().UTC()
	fullPath, relative, err := s.resolve(req.Path)
	if err != nil {
		return WriteResult{}, err
	}
	if err := contextErr(ctx); err != nil {
		return WriteResult{}, err
	}

	current, err := os.ReadFile(fullPath)
	if err != nil && !os.IsNotExist(err) {
		return WriteResult{}, err
	}
	currentRevision := revisionOf(current)
	if req.BaseRevision != "" && req.BaseRevision != currentRevision {
		return WriteResult{}, fmt.Errorf("%w: %s", ErrRevisionConflict, relative)
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return WriteResult{}, err
	}
	if err := os.WriteFile(fullPath, []byte(req.Content), 0644); err != nil {
		return WriteResult{}, err
	}

	revision := revisionOf([]byte(req.Content))
	return WriteResult{
		Revision: revision,
		Operation: types.WorkspaceOperation{
			OperationID:  newOperationID(),
			TurnID:       req.TurnID,
			Kind:         "write",
			Path:         relative,
			Revision:     revision,
			BaseRevision: req.BaseRevision,
			Summary:      fmt.Sprintf("write %s", relative),
			StartedAt:    start,
			FinishedAt:   time.Now().UTC(),
		},
	}, nil
}

type CommandRequest struct {
	TurnID  string
	Command string
	Args    []string
	Stdin   string
}

type CommandResult struct {
	Stdout    string
	Stderr    string
	ExitCode  int
	Operation types.WorkspaceOperation
}

func (s *Service) Run(ctx context.Context, req CommandRequest) (CommandResult, error) {
	start := time.Now().UTC()
	if strings.TrimSpace(req.Command) == "" {
		return CommandResult{}, errors.New("command is required")
	}

	var cmd *exec.Cmd
	if len(req.Args) == 0 {
		cmd = exec.CommandContext(ctx, "/bin/sh", "-c", req.Command)
	} else {
		cmd = exec.CommandContext(ctx, req.Command, req.Args...)
	}
	cmd.Dir = s.root
	cmd.Stdin = strings.NewReader(req.Stdin)
	var stdout, stderr strings.Builder
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
	result := CommandResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Operation: types.WorkspaceOperation{
			OperationID: newOperationID(),
			TurnID:      req.TurnID,
			Kind:        "test",
			Summary:     fmt.Sprintf("run command %s", req.Command),
			StartedAt:   start,
			FinishedAt:  time.Now().UTC(),
		},
	}
	return result, err
}

func (s *Service) Glob(pattern string) ([]string, error) {
	if strings.TrimSpace(pattern) == "" {
		return nil, errors.New("glob pattern is required")
	}
	candidate := pattern
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.root, candidate)
	}
	absolute, err := filepath.Abs(candidate)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(s.root, filepath.Clean(absolute))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("%w: %s", ErrPathOutsideWorkspace, pattern)
	}

	matches, err := filepath.Glob(absolute)
	if err != nil {
		return nil, err
	}
	for i, match := range matches {
		if relativeMatch, relErr := filepath.Rel(s.root, match); relErr == nil {
			matches[i] = filepath.ToSlash(relativeMatch)
		}
	}
	return matches, nil
}

func (s *Service) resolve(path string) (string, string, error) {
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
	return clean, filepath.ToSlash(relative), nil
}

func revisionOf(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

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
