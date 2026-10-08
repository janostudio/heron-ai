package definitions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/heron-ai/heron-engine/internal/config"
	"github.com/heron-ai/heron-engine/pkg/types"
)

// Mode selects how a request treats an already-existing definition.
type Mode string

const (
	// ModeCreate fails if the target definition already exists.
	ModeCreate Mode = "create"
	// ModeUpsert merges the spec into the existing definition.
	ModeUpsert Mode = "upsert"
)

// CreateAgentRequest is one `create_agent` call.
//
// Template names a built-in template to start from ("" means the built-in
// default shape), and Spec holds the model-supplied fields merged on top of it.
// Both are generic maps because the model writes the spec as JSON/YAML and
// because the merge must tell "absent" from "explicitly zero" — a typed struct
// cannot express that difference.
type CreateAgentRequest struct {
	Mode     Mode
	Template string
	Spec     map[string]any
}

// CreateTeamRequest is one `create_team` call.
//
// One request can touch three kinds of file: the team definition, an agent
// definition for every call naming an agent the tree does not have yet, and a
// binding in the current flow. Doing it in one call is deliberate — a team
// whose calls point at agents nobody created is an unloadable tree, so
// splitting this across calls would leave the tree broken in between.
type CreateTeamRequest struct {
	Mode     Mode
	Template string
	Spec     map[string]any
}

// ApplyResult describes what an apply actually did. Files are absolute paths in
// commit order (referenced definitions before the flow that binds them).
type ApplyResult struct {
	Action        string
	Mode          string
	Files         []string
	CreatedAgents []string
	UpdatedAgents []string
	BoundToFlow   string
	Reloaded      bool
}

// Writer turns a definition spec into validated files on disk and publishes the
// result to the live runtime.
//
// Safe for concurrent use: every apply serializes on mu. Definition creation is
// a read-modify-write of a whole config tree (snapshot, merge, stage, validate,
// commit, reload), and two overlapping applies would interleave those phases —
// the second would merge against a snapshot the first has already invalidated,
// and its rollback image would be the first apply's output. The critical
// section is short (a directory copy plus a handful of renames), so one mutex
// is the right tool; per-request worktrees would buy nothing because the commit
// still lands in the single real tree.
type Writer struct {
	store *types.DefinitionStore

	// loader builds a config loader that validates a tree rooted at an absolute
	// config root. It is a field rather than a direct config.NewConfigLoader
	// call so tests can substitute a fake. A config loader resolves relative
	// paths against its base dir, so "which base for which root" is exactly the
	// thing that has to stay explicit — see loaderForRoot.
	loader func(root string) *config.ConfigLoader

	mu sync.Mutex

	// afterStage runs inside apply once the plan has been staged but before any
	// byte is committed to the real tree. Tests use it as a crash/race
	// injection seam — it is the only moment a candidate tree exists while the
	// real tree is still untouched. Unexported on purpose: a test hook is not
	// part of the tool's contract.
	afterStage func() error
}

// NewWriter builds a Writer over the store whose tree it will extend.
//
// The store supplies both the identity of the tree to read (Snapshot) and the
// absolute root and flow path to write back to, so a writer can never be aimed
// at a different config than the engine is running.
func NewWriter(store *types.DefinitionStore) *Writer {
	return &Writer{store: store, loader: loaderForRoot}
}

// loaderForRoot builds a loader for the tree rooted at configRoot.
//
// The base directory is the root's PARENT, not the root itself. ConfigLoader
// has two families of reads with different anchors:
//
//   - definition reads (flows/, teams/, agents/, skills/, rules/) resolve
//     against the config root, which LoadDefinitions derives from the flow
//     path on its own;
//   - settings reads (LoadRuntimeLimits, LoadKnowledgeSettings,
//     LoadLoggingSettings) resolve the literal ".agents/settings.json" against
//     the loader's base directory.
//
// Rooting at the config root makes the settings reads look for
// "<configRoot>/.agents/settings.json", which does not exist, and the loader
// silently falls back to RuntimeLimits defaults. Because apply() publishes the
// validated tree, that would reset the running engine's limits on every Define
// call — measured at 3 -> 200 for max_agent_rounds. The parent makes
// ".agents/settings.json" resolve to "<configRoot>/settings.json", the file the
// engine actually loaded at startup.
//
// The same reasoning, and the same fix, applies to internal/app's reload path;
// the two must agree or a reload produces a tree the engine is not running.
func loaderForRoot(configRoot string) *config.ConfigLoader {
	return config.NewConfigLoader(filepath.Dir(configRoot))
}

// newWriterWithLoader is the test seam for NewWriter: it keeps the store
// pointing at the real tree while letting the loader be rooted elsewhere.
func newWriterWithLoader(store *types.DefinitionStore, loader func(string) *config.ConfigLoader) *Writer {
	return &Writer{store: store, loader: loader}
}

// CreateAgent writes one agent definition.
//
// By explicit product decision this touches nothing else: creating an agent
// does not add it to any team and does not bind it into the flow. An agent on
// its own is a valid, loadable tree (simply unreferenced), so the caller stays
// free to wire it up — or not — in a later call.
func (w *Writer) CreateAgent(ctx context.Context, req CreateAgentRequest) (*ApplyResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	root, flowPath, cur, err := w.prepare()
	if err != nil {
		return nil, err
	}

	name, err := specName(req.Spec)
	if err != nil {
		return nil, err
	}
	if err := validateDefinitionName(name, "agent"); err != nil {
		return nil, err
	}

	existing, exists := cur.Agents[name]
	if req.Mode == ModeUpsert {
		if !exists {
			return nil, fmt.Errorf("agent %q does not exist: use mode create to add it", name)
		}
		if err := checkNoRename(existing.Name, name, "agent"); err != nil {
			return nil, err
		}
	} else if err := checkAgentNameAvailable(cur.Agents, name); err != nil {
		return nil, err
	}

	spec, body, err := prepareAgentSpec(req, name, existing, exists)
	if err != nil {
		return nil, err
	}
	if err := checkUnknownAgentSpecKeys(spec); err != nil {
		return nil, err
	}
	agent, err := decodeAgent(spec, body)
	if err != nil {
		return nil, fmt.Errorf("agent %q: %w", name, err)
	}
	if err := checkAgentSpecSupport(agent, cur.Skills, knowledgeIDs(root)); err != nil {
		return nil, err
	}

	data, err := renderAgent(spec, body)
	if err != nil {
		return nil, fmt.Errorf("agent %q: %w", name, err)
	}

	plan := &WritePlan{}
	if err := plan.Add(WriteOp{
		Path:       agentFilePath(name),
		Data:       data,
		Mode:       pickWriteMode(exists),
		CreatesDir: true,
	}); err != nil {
		return nil, err
	}

	result := &ApplyResult{Action: "create_agent", Mode: modeLabel(exists)}
	if exists {
		result.UpdatedAgents = []string{name}
	} else {
		result.CreatedAgents = []string{name}
	}

	if err := w.apply(ctx, root, flowPath, plan, result); err != nil {
		return nil, err
	}
	return result, nil
}

// CreateTeam writes one team definition, plus an agent definition for every
// call naming an agent the tree does not have, plus a binding in the current
// flow.
func (w *Writer) CreateTeam(ctx context.Context, req CreateTeamRequest) (*ApplyResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	root, flowPath, cur, err := w.prepare()
	if err != nil {
		return nil, err
	}

	plan := &WritePlan{}
	result := &ApplyResult{Action: "create_team"}

	teamID, err := specName(req.Spec)
	if err != nil {
		return nil, err
	}
	if err := validateDefinitionName(teamID, "team"); err != nil {
		return nil, err
	}

	existing, exists := cur.Teams[teamID]
	if req.Mode == ModeUpsert {
		if !exists {
			return nil, fmt.Errorf("team %q does not exist: use mode create to add it", teamID)
		}
		if err := checkNoRename(existing.ID, teamID, "team"); err != nil {
			return nil, err
		}
	} else {
		if err := checkTeamNameAvailable(cur.Teams, teamID); err != nil {
			return nil, err
		}
		// A create whose canonical filename is already claimed by a DIFFERENT
		// team definition would silently overwrite it, turning the create into
		// an upsert of somebody else's team.
		if owner := teamFileFor(root, teamID); owner != "" {
			return nil, fmt.Errorf(
				"team file %s already defines a different team: cannot create team %q there",
				owner,
				teamID,
			)
		}
	}

	team, teamData, err := buildTeam(req, teamID, existing, exists)
	if err != nil {
		return nil, err
	}
	if err := checkCallIDsMatchKeys(team); err != nil {
		return nil, err
	}
	if err := checkCallDependencies(team); err != nil {
		return nil, err
	}

	// Agents introduced by THIS spec's calls that the tree does not have yet.
	// They are created in the same apply so the staged tree validates: a call
	// pointing at a missing agent fails the loader's dangling-reference check.
	// The spec's own calls are used, not the merged team's, so an upsert cannot
	// invent agents for calls it never mentioned (see inlineAgentSpecs).
	specCalls, err := rawSpecCalls(req.Spec)
	if err != nil {
		return nil, err
	}
	inlineAgents, err := inlineAgentSpecs(req.Spec, specCalls, cur)
	if err != nil {
		return nil, err
	}
	for _, name := range sortedKeys(inlineAgents) {
		if err := validateDefinitionName(name, "agent"); err != nil {
			return nil, err
		}
		spec, body, err := agentSpecFromMap(inlineAgents[name], templateFor(inlineAgents[name], "agent.generic"))
		if err != nil {
			return nil, fmt.Errorf("inline agent %q: %w", name, err)
		}
		if err := checkUnknownAgentSpecKeys(spec); err != nil {
			return nil, fmt.Errorf("inline agent %q: %w", name, err)
		}
		agent, err := decodeAgent(spec, body)
		if err != nil {
			return nil, fmt.Errorf("inline agent %q: %w", name, err)
		}
		if err := checkAgentSpecSupport(agent, cur.Skills, knowledgeIDs(root)); err != nil {
			return nil, err
		}
		data, err := renderAgent(spec, body)
		if err != nil {
			return nil, fmt.Errorf("inline agent %q: %w", name, err)
		}
		if err := plan.Add(WriteOp{
			Path:       agentFilePath(name),
			Data:       data,
			Mode:       WriteCreate,
			CreatesDir: true,
		}); err != nil {
			return nil, err
		}
		result.CreatedAgents = append(result.CreatedAgents, name)
	}

	// A team's ID and its filename can differ (id: qa_team in teams/qa.yml), so
	// an upsert must write back to the file that actually holds the definition.
	// Deriving the path from the ID would mint a second file for the same team
	// and leave the original — the one the flow references — frozen.
	teamPath := teamFilePath(teamID)
	if exists {
		if found := teamFileFor(root, teamID); found != "" {
			teamPath = found
		}
	}
	if err := plan.Add(WriteOp{
		Path: teamPath,
		Data: teamData,
		Mode: pickWriteMode(exists),
	}); err != nil {
		return nil, err
	}

	// The flow binding, if asked for. Read-modify-write through types.Flow
	// rather than a text append: yaml.Marshal emits map keys in sorted order, so
	// the result is deterministic and a diff stays readable, and the parsed form
	// is exactly what the loader will read on the next start.
	flowData, _, err := w.planFlowBinding(cur, req.Spec)
	if err != nil {
		return nil, err
	}
	if flowData != nil {
		rel, err := flowRelativePath(flowPath)
		if err != nil {
			return nil, err
		}
		if err := plan.Add(WriteOp{Path: rel, Data: flowData, Mode: WriteReplace}); err != nil {
			return nil, err
		}
		result.BoundToFlow = cur.Flow.ID
	}

	result.Mode = modeLabel(exists)

	if err := w.apply(ctx, root, flowPath, plan, result); err != nil {
		return nil, err
	}
	return result, nil
}

// prepare locks in the three things every apply needs: where to write, and the
// tree the request is being merged into.
func (w *Writer) prepare() (string, string, *types.Definitions, error) {
	if w == nil || w.store == nil {
		return "", "", nil, errors.New("writer has no definition store")
	}
	root := strings.TrimSpace(w.store.ConfigRoot())
	if root == "" {
		return "", "", nil, errors.New("definition store has no config root: writer cannot locate the config tree")
	}
	flowPath := strings.TrimSpace(w.store.FlowPath())
	if flowPath == "" {
		return "", "", nil, errors.New("definition store has no flow path: writer cannot bind new teams")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve config root %q: %w", root, err)
	}
	absFlow, err := filepath.Abs(flowPath)
	if err != nil {
		return "", "", nil, fmt.Errorf("resolve flow path %q: %w", flowPath, err)
	}
	cur := w.store.Snapshot()
	if cur == nil {
		return "", "", nil, errors.New("definition store holds no definitions")
	}
	return absRoot, absFlow, cur, nil
}

// apply is the shared 8-step pipeline. Both CreateAgent and CreateTeam build a
// plan and hand it here; from this point the two are identical, which is what
// keeps the safety properties in one place instead of duplicated per action.
func (w *Writer) apply(
	ctx context.Context,
	root string,
	flowPath string,
	plan *WritePlan,
	result *ApplyResult,
) error {
	// Step 1: read the current bytes of every planned file. They serve three
	// jobs — the rollback image, the compare-and-swap baseline the commit
	// re-reads against, and (for upserts) the merge baseline, which was already
	// consumed while the plan was being built.
	if err := plan.captureOriginals(root); err != nil {
		return err
	}

	// Step 3: build a shadow tree. Config trees are small, so copying the whole
	// thing is affordable, and it is what makes step 4 meaningful: the loader
	// validates exactly the tree that would exist if the commit succeeded,
	// while concurrent LoadDefinitions calls keep seeing a complete tree
	// instead of a half-written one.
	staging := filepath.Join(root, fmt.Sprintf(".heron-staging-%d", time.Now().UnixNano()))
	if err := copyConfigTree(root, staging); err != nil {
		return fmt.Errorf("stage config tree: %w", err)
	}
	// Step 8: the staging tree never survives an apply, successful or not.
	defer func() { _ = os.RemoveAll(staging) }()

	if err := plan.applyTo(staging); err != nil {
		return err
	}

	if w.afterStage != nil {
		if err := w.afterStage(); err != nil {
			return err
		}
	}

	// Step 4: validate the candidate through the real loader. That single call
	// runs the whole chain — flow, teams, agents, skills, rules — so the writer
	// never re-implements config semantics. The checks in validate.go cover only
	// what the loader cannot see: whether a rule is violated BY THIS CHANGE,
	// such as a newly added second coordinator or a newly claimed binding key.
	stagingFlow, err := stagingPath(staging, root, flowPath)
	if err != nil {
		return err
	}
	if _, err := w.loader(staging).LoadDefinitions(ctx, config.DefinitionsLoadRequest{
		FlowPath: stagingFlow,
	}); err != nil {
		return fmt.Errorf("candidate config is invalid: %w", err)
	}

	// Step 5: commit, referenced files first. Optimistic concurrency: each file
	// is re-read and compared against what step 1 saw, so a Define racing a
	// human edit aborts instead of silently discarding the edit.
	committed, commitErr := commitPlan(root, plan)
	if commitErr != nil {
		// Step 7: restore what already landed, newest write first, so the tree
		// never sits in a state that does not load. A rollback that itself fails
		// must name the files left behind — that is the one outcome a human has
		// to finish by hand.
		if rb := rollback(root, committed); !rb.OK() {
			return fmt.Errorf("%w; %s", commitErr, rb.Error())
		}
		return commitErr
	}

	result.Files = absolutePaths(root, plan.InCommitOrder())

	// Step 6: publish. The loader here is rooted at the REAL tree, not staging.
	loader := w.loader(root)
	if err := w.store.Reload(ctx, func(ctx context.Context) (*types.Definitions, error) {
		return loader.LoadDefinitions(ctx, config.DefinitionsLoadRequest{FlowPath: flowPath})
	}); err != nil {
		// No rollback. The files passed validation moments ago, so a failure
		// here means something outside this writer changed between step 4 and
		// now; the committed tree is the one the user asked for, and undoing it
		// would throw their change away on the strength of an unrelated
		// failure. The error names the files so the situation is not silent.
		return fmt.Errorf(
			"definitions were written to %s but reloading them failed (%w): the new files are on disk, reload or restart to pick them up",
			strings.Join(result.Files, ", "),
			err,
		)
	}
	result.Reloaded = true
	return nil
}

// planFlowBinding turns a `bind` block into a replacement for the current flow
// file, or nil when the spec asks for no binding.
func (w *Writer) planFlowBinding(cur *types.Definitions, spec map[string]any) ([]byte, string, error) {
	raw, ok := spec["bind"]
	if !ok || raw == nil {
		return nil, "", nil
	}
	bind, ok := raw.(map[string]any)
	if !ok {
		return nil, "", errors.New("spec.bind must be a mapping")
	}

	key, _ := bind["key"].(string)
	if strings.TrimSpace(key) == "" {
		key, _ = spec["bind_key"].(string)
	}
	teamID, _ := spec["id"].(string)
	if strings.TrimSpace(teamID) == "" {
		teamID, _ = spec["name"].(string)
	}
	key = strings.TrimSpace(key)
	teamID = strings.TrimSpace(teamID)
	if key == "" {
		return nil, "", errors.New("spec.bind.key is required when bind is set")
	}
	if teamID == "" {
		return nil, "", errors.New("spec.bind requires the team id")
	}

	if err := checkFlowBindingKeyAvailable(cur.Flow, key, teamID); err != nil {
		return nil, "", err
	}

	binding := types.FlowTeamBinding{ID: key, TeamID: teamID}
	if err := decodeInto(bindField(bind, "coordinator"), &binding.Coordinator); err != nil {
		return nil, "", fmt.Errorf("spec.bind.coordinator: %w", err)
	}
	if err := decodeInto(bindField(bind, "can_activate"), &binding.CanActivate); err != nil {
		return nil, "", fmt.Errorf("spec.bind.can_activate: %w", err)
	}
	if err := decodeInto(bindField(bind, "depends_on"), &binding.DependsOn); err != nil {
		return nil, "", fmt.Errorf("spec.bind.depends_on: %w", err)
	}
	if err := decodeInto(bindField(bind, "inputs"), &binding.Inputs); err != nil {
		return nil, "", fmt.Errorf("spec.bind.inputs: %w", err)
	}
	if onProceed := bindField(bind, "on_proceed"); onProceed != nil {
		// on_proceed accepts both the documented list shorthand and the full
		// routing object, so it goes through the same yaml round trip as the
		// other sub-fields rather than a direct struct assignment.
		if err := decodeInto(onProceed, &binding.OnProceed); err != nil {
			return nil, "", fmt.Errorf("spec.bind.on_proceed: %w", err)
		}
	}

	// Copy the flow before mutating it. Snapshot() hands out the live tree by
	// contract, and writing through it would race every other turn reading the
	// same maps.
	flow := types.Flow{
		ID:          cur.Flow.ID,
		EntryTeamID: cur.Flow.EntryTeamID,
		Workspace:   cur.Flow.Workspace,
		Teams:       make(map[string]types.FlowTeamBinding, len(cur.Flow.Teams)+1),
	}
	for name, existing := range cur.Flow.Teams {
		flow.Teams[name] = existing
	}
	flow.Teams[key] = binding

	// The extra coordinator check, run here as well as against the reloaded tree
	// so the failure names the binding instead of arriving wrapped in a loader
	// error after the files exist.
	if err := checkSingleCoordinator(flow); err != nil {
		return nil, "", err
	}

	data, err := marshalYAML(flow)
	if err != nil {
		return nil, "", fmt.Errorf("encode flow %q: %w", flow.ID, err)
	}
	return data, flow.ID, nil
}

// bindField returns bind[name], distinguishing absent from explicitly-null.
func bindField(bind map[string]any, name string) any {
	value, ok := bind[name]
	if !ok || value == nil {
		return nil
	}
	return value
}

// validateDefinitionName rejects names that would escape the config root or
// produce a nonsense path before any of them reach safeJoin.
//
// The name becomes a path segment (agents/<name>/AGENT.md, teams/<id>.yml), so
// a name with a separator, a parent reference or an absolute prefix is a
// directory-traversal attempt, not a typo. Rejecting it here means the error
// names the bad input rather than the derived path.
func validateDefinitionName(name, kind string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("%s name is required", kind)
	}
	if filepath.IsAbs(name) {
		return fmt.Errorf("%s name %q must not be an absolute path", kind, name)
	}
	clean := filepath.Clean(name)
	if clean != name || clean == "." || clean == ".." ||
		strings.ContainsRune(name, filepath.Separator) ||
		strings.Contains(name, "/") || strings.Contains(name, `\`) {
		return fmt.Errorf("%s name %q must not contain a path separator", kind, name)
	}
	return nil
}

// agentFilePath is the canonical directory form: agents/<name>/AGENT.md.
//
// The directory form rather than a flat <name>.md: the loader reads both, but
// only the directory form gives private knowledge and rules a stable home
// (agents/<name>/knowledge, agents/<name>/rules), which is what a
// model-created agent is most likely to grow next.
func agentFilePath(name string) string {
	return filepath.Join("agents", name, "AGENT.md")
}

func teamFilePath(id string) string {
	return filepath.Join("teams", id+".yml")
}

// flowRelativePath is the flow file's location relative to the config root, so
// the plan stays replayable against the staging tree.
func flowRelativePath(flowPath string) (string, error) {
	root := config.ConfigRootForFlow(flowPath)
	rel, err := filepath.Rel(root, flowPath)
	if err != nil {
		return "", fmt.Errorf("locate flow %s under %s: %w", flowPath, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("flow %s is outside its config root %s", flowPath, root)
	}
	return rel, nil
}

func pickWriteMode(exists bool) WriteMode {
	if exists {
		return WriteReplace
	}
	return WriteCreate
}

func modeLabel(exists bool) string {
	if exists {
		return "updated"
	}
	return "created"
}

// absolutePaths maps config-root-relative plan paths back to absolute ones for
// the result, in commit order.
func absolutePaths(root string, ops []WriteOp) []string {
	paths := make([]string, 0, len(ops))
	for _, op := range ops {
		target, err := safeJoin(root, op.Path)
		if err != nil {
			continue
		}
		paths = append(paths, target)
	}
	return paths
}

// specName pulls the required name/id out of a spec.
//
// team specs use `id` and agent specs use `name`; both are accepted for both so
// a model that reaches for the other spelling gets a working call rather than a
// confusing "name is required".
func specName(spec map[string]any) (string, error) {
	raw, ok := spec["name"]
	if !ok || raw == nil {
		raw, ok = spec["id"]
	}
	if !ok || raw == nil {
		return "", errors.New("spec.name is required")
	}
	name, ok := raw.(string)
	if !ok || strings.TrimSpace(name) == "" {
		return "", errors.New("spec.name must be a non-empty string")
	}
	return strings.TrimSpace(name), nil
}

// sortedKeys returns map keys in sorted order so plans, results and the files
// they produce are deterministic.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
