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
	Read(ctx context.Context, req ReadRequest) (ReadResult, error)
	Write(ctx context.Context, req WriteRequest) (WriteResult, error)
	Run(ctx context.Context, req CommandRequest) (CommandResult, error)
	Glob(pattern string) ([]string, error)
	GlobWithOptions(ctx context.Context, req GlobRequest) ([]string, error)
	Search(ctx context.Context, req SearchRequest) (SearchResult, error)
}

// New constructs a Workspace from a configuration. It is the unified entry
// point for building the execution backend. This iteration implements only the
// local branch; the ssh branch returns a "not implemented" error and is wired
// up in the next step.
func New(cfg types.WorkspaceConfig, localRoot string) (Workspace, error) {
	switch cfg.Type {
	case "local", "":
		return NewLocal(localRoot)
	case "ssh":
		return nil, fmt.Errorf("workspace type %q is not implemented yet", cfg.Type)
	default:
		return nil, fmt.Errorf("unsupported workspace type %q", cfg.Type)
	}
}
