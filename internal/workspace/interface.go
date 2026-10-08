package workspace

import (
	"context"
	"fmt"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// Workspace is the execution backend interface. Local (current) and remote
// (ssh/docker/...) backends both implement it. It abstracts file operations
// (Read/Write/Glob/Search) and command execution (Run); engine state
// (session/knowledge/state/logging) remains local and does NOT go through this
// interface.
type Workspace interface {
	Root() string
	ResolvePathForTool(path string) (string, string, error)
	// ResolvePathForToolRestricted is ResolvePathForTool with a path
	// restriction applied. Tools that hand a path to an external helper (an
	// indexer, a language server) must use this overload: the helper reads
	// the filesystem itself, so validating only that the path is inside the
	// workspace says nothing about whether the caller may see it.
	//
	// It is a separate method rather than a parameter on
	// ResolvePathForTool so the existing signature stays intact for callers
	// with no restriction to express.
	ResolvePathForToolRestricted(path string, restrict Restriction) (string, string, error)
	Read(ctx context.Context, req ReadRequest) (ReadResult, error)
	Write(ctx context.Context, req WriteRequest) (WriteResult, error)
	Run(ctx context.Context, req CommandRequest) (CommandResult, error)
	Glob(pattern string) ([]string, error)
	GlobWithOptions(ctx context.Context, req GlobRequest) ([]string, error)
	Search(ctx context.Context, req SearchRequest) (SearchResult, error)
	// IsRemote reports whether this backend executes on a remote host (e.g.
	// SSH). Tools that rely on a local index/helper (CodeNav/codels) can use
	// this to degrade gracefully.
	IsRemote() bool
}

// New constructs a Workspace from a configuration. It is the unified entry
// point for building the execution backend.
func New(cfg types.WorkspaceConfig, localRoot string) (Workspace, error) {
	switch cfg.Type {
	case "local", "":
		return NewLocal(localRoot)
	case "ssh":
		if cfg.SSH == nil {
			return nil, fmt.Errorf("workspace type %q requires an ssh config", cfg.Type)
		}
		return newSSHWorkspace(*cfg.SSH)
	default:
		return nil, fmt.Errorf("unsupported workspace type %q", cfg.Type)
	}
}
