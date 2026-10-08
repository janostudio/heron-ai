package definitions

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// This file holds the merge and filesystem mechanics the writer is built from:
// the map[string]any funnel that makes "absent" distinguishable from "explicitly
// zero", the staging copy, and the commit/rollback pair.

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

// deepCopyMap clones a decoded YAML document. Needed because a template's maps
// are shared across every instantiation (the embedded FS is read once) and
// mutating one would leak the first caller's spec into every later template.
func deepCopyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = deepCopyValue(value)
	}
	return dst
}

func deepCopyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return deepCopyMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i, item := range typed {
			cloned[i] = deepCopyValue(item)
		}
		return cloned
	default:
		return value
	}
}

// deepMergeMap merges overlay onto base and returns a new map.
//
// The rules are deliberately two, not a hierarchy of special cases:
//
//   - nested objects (map[string]any) merge by key, recursively
//   - everything else — scalars, lists, null — is a leaf and overwrites
//
// Lists overwrite wholesale rather than concatenating because these specs carry
// sets whose meaning changes with membership: tools.builtin is a capability
// list, skills/knowledge/rules are resolved by name, and a union would make it
// impossible to ever remove an entry. "Supplied replaces, absent keeps" is the
// only rule a model can predict without reading the writer.
func deepMergeMap(base, overlay map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	result := deepCopyMap(base)
	for key, value := range overlay {
		if existing, ok := result[key]; ok {
			existingMap, existingIsMap := existing.(map[string]any)
			valueMap, valueIsMap := value.(map[string]any)
			if existingIsMap && valueIsMap {
				result[key] = deepMergeMap(existingMap, valueMap)
				continue
			}
		}
		result[key] = deepCopyValue(value)
	}
	return result
}

// mergeAgentSpec folds a request over an existing agent document.
//
// The funnel through map[string]any is the whole point. Decoding the file into
// types.AgentConfig and merging structs would lose the difference between
// `temperature: 0` (the model asked for greedy sampling) and an omitted
// temperature (keep whatever is there) — both arrive as the zero value, and
// ModelConfig.Temperature is a *float64 precisely because the config format
// already needed that distinction. Merging maps keeps it all the way to the
// re-encode.
//
// Lists are replaced wholesale here for the same reason as in deepMergeMap;
// this function exists separately so the "which fields are lists" decision is
// visible in one place rather than hidden inside a generic merge.
func mergeAgentSpec(existing, spec map[string]any) map[string]any {
	return deepMergeMap(existing, spec)
}

// mergeTeamSpec folds a team request over an existing team document, with calls
// merged BY NAME rather than wholesale.
//
// Calls get the exception because a team's call map is additive in normal use:
// adding one call to an existing team is the common case, and replacing the map
// would silently drop every other call (and with it every depends_on and output
// reference into the dropped ones). Same-name calls are overwritten, so a spec
// can still correct a single call.
func mergeTeamSpec(existing, spec map[string]any) map[string]any {
	result := deepMergeMap(existing, spec)

	existingCalls, _ := existing["calls"].(map[string]any)
	specCalls, _ := spec["calls"].(map[string]any)
	if len(existingCalls) == 0 || len(specCalls) == 0 {
		return result
	}

	merged := make(map[string]any, len(existingCalls)+len(specCalls))
	for name, call := range existingCalls {
		merged[name] = deepCopyValue(call)
	}
	for name, call := range specCalls {
		if previous, ok := merged[name]; ok {
			previousMap, previousIsMap := previous.(map[string]any)
			callMap, callIsMap := call.(map[string]any)
			if previousIsMap && callIsMap {
				merged[name] = deepMergeMap(previousMap, callMap)
				continue
			}
		}
		merged[name] = deepCopyValue(call)
	}
	result["calls"] = merged
	return result
}

// prepareAgentSpec assembles the map that will be rendered for an agent, and
// returns it alongside the markdown body.
//
// The returned map is always the merged document, so the caller can both
// re-encode it and decode it into types.AgentConfig. Decoding it is what keeps
// generated files honest: a spec that would not load as a real Agent definition
// fails here, before anything reaches the disk.
func prepareAgentSpec(
	req CreateAgentRequest,
	name string,
	existing types.AgentConfig,
	exists bool,
) (map[string]any, string, error) {
	base := map[string]any{}
	switch {
	case exists:
		// The merge baseline is the file on disk, decoded to a generic map.
		// Using the typed struct would reintroduce the absent-vs-zero problem
		// this funnel exists to avoid.
		baseline, err := agentDocumentOf(existing)
		if err != nil {
			return nil, "", err
		}
		base = baseline
	case req.Template != "":
		tmpl, err := LoadTemplate(req.Template)
		if err != nil {
			return nil, "", err
		}
		if tmpl.Kind != KindAgent {
			return nil, "", fmt.Errorf("template %q is not an agent template", req.Template)
		}
		base = deepCopyMap(tmpl.Frontmatter)
	default:
		base = defaultAgentBase()
	}

	spec := sanitizeAgentSpec(req.Spec)
	merged := mergeAgentSpec(base, spec)
	merged["name"] = name

	body := ""
	if exists {
		body = existing.Body
	} else if req.Template != "" {
		tmpl, err := LoadTemplate(req.Template)
		if err != nil {
			return nil, "", err
		}
		body = tmpl.Body
	}
	// body is a plain markdown field, not frontmatter: it is pulled out of the
	// spec before merging so it never ends up encoded as a YAML key.
	if rawBody, ok := req.Spec["body"]; ok && rawBody != nil {
		text, ok := rawBody.(string)
		if !ok {
			return nil, "", errors.New("spec.body must be a string")
		}
		body = text
	}
	return merged, body, nil
}

// agentSpecFromMap is prepareAgentSpec's kernel for the inline agents a
// create_team call generates: no existing definition, optional template.
func agentSpecFromMap(spec map[string]any, templateName string) (map[string]any, string, error) {
	name, err := specName(spec)
	if err != nil {
		return nil, "", err
	}

	base := defaultAgentBase()
	body := ""
	if templateName != "" {
		tmpl, err := LoadTemplate(templateName)
		if err != nil {
			return nil, "", err
		}
		base = deepCopyMap(tmpl.Frontmatter)
		body = tmpl.Body
	}

	cleaned := sanitizeAgentSpec(spec)
	merged := mergeAgentSpec(base, cleaned)
	merged["name"] = name

	if rawBody, ok := spec["body"]; ok && rawBody != nil {
		text, ok := rawBody.(string)
		if !ok {
			return nil, "", errors.New("spec.body must be a string")
		}
		body = text
	}
	return merged, body, nil
}

// sanitizeAgentSpec drops the keys that are not part of an Agent definition.
//
// The spec is model-authored, so it routinely carries writer directives
// (`body`, `template`, `agent_specs`) next to real agent fields. Dropping them
// here — rather than teaching the encoder to skip unknown keys — keeps the
// window between "spec as written" and "file on disk" narrow enough that a
// silently-lost field is a real error, which checkUnknownAgentKeys is there to
// report.
func sanitizeAgentSpec(spec map[string]any) map[string]any {
	cleaned := map[string]any{}
	for key, value := range spec {
		switch key {
		case "body", "template", "agent_specs", "bind", "bind_key", "calls",
			"goal", "state", "output", "outputs", "id":
			continue
		default:
			cleaned[key] = deepCopyValue(value)
		}
	}
	return cleaned
}

// defaultAgentBase is the fallback when no template is named. It mirrors the
// shape of agent.generic.md closely enough that a spec which only sets `name`
// still loads: the loader requires a name, and everything else has a sane zero.
func defaultAgentBase() map[string]any {
	return map[string]any{
		"tools": map[string]any{
			"builtin": []any{"Read", "Grep", "Glob", "TodoWrite", "TodoRead"},
		},
		"loop": map[string]any{"max_rounds": 14, "tool_execution": "sequential"},
	}
}

// agentDocumentOf re-decodes a typed agent into a generic document. It is the
// bridge from the read-only snapshot (which holds typed values) to the merge
// funnel (which needs maps).
//
// Body is excluded: it is carried separately so it can never be mistaken for a
// frontmatter key.
func agentDocumentOf(agent types.AgentConfig) (map[string]any, error) {
	encoded, err := marshalYAML(agent)
	if err != nil {
		return nil, fmt.Errorf("encode agent %q: %w", agent.Name, err)
	}
	document := map[string]any{}
	if err := yaml.Unmarshal(encoded, &document); err != nil {
		return nil, fmt.Errorf("decode agent %q: %w", agent.Name, err)
	}
	return document, nil
}

// inlineAgentSpecs finds the agents that THIS SPEC introduces and the tree does
// not have yet, and gathers the spec to create each one.
//
// Scoped to the spec's own calls, not to the merged team: an upsert that only
// changes `goal` must not conjure agents for calls it never mentioned. Those
// calls belong to the existing definition, and if one of them names a missing
// agent that is a pre-existing defect the loader will report — inventing an
// agent to silence it would hide the problem behind a stub nobody wrote.
//
// The generic template is the fallback, not a default worth hiding: a spec that
// names no template for an inline agent still gets a working agent, because a
// create_team call whose team cannot load is worse than a generic teammate.
func inlineAgentSpecs(spec map[string]any, specCalls map[string]any, cur *types.Definitions) (map[string]map[string]any, error) {
	needed := map[string]map[string]any{}

	for _, key := range sortedKeys(specCalls) {
		call, ok := specCalls[key].(map[string]any)
		if !ok {
			continue
		}
		callType, _ := call["type"].(string)
		if callType != "" && callType != string(types.CallAgent) {
			continue
		}
		name, _ := call["agent"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := cur.Agents[name]; exists {
			continue
		}
		if _, already := needed[name]; already {
			continue
		}
		needed[name] = map[string]any{"name": name}
	}

	if len(needed) == 0 {
		return needed, nil
	}

	rawSpecs, ok := spec["agent_specs"]
	if !ok || rawSpecs == nil {
		return needed, nil
	}
	agentSpecs, ok := rawSpecs.(map[string]any)
	if !ok {
		return nil, errors.New("spec.agent_specs must be a mapping of agent name to spec")
	}
	for name := range needed {
		raw, ok := agentSpecs[name]
		if !ok || raw == nil {
			continue
		}
		agentSpec, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("spec.agent_specs.%s must be a mapping", name)
		}
		merged := deepCopyMap(agentSpec)
		merged["name"] = name
		needed[name] = merged
	}
	return needed, nil
}

// templateFor picks the template named inside an inline agent spec, falling
// back to the caller's default. A create_team call whose team cannot load is
// worse than a generic teammate, so the default is applied rather than raising
// "no template".
func templateFor(spec map[string]any, fallback string) string {
	if name, ok := spec["template"].(string); ok && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return fallback
}

// decodeAgent decodes a merged document into the typed config the loader will
// produce, so an invalid spec fails before it is written.
func decodeAgent(spec map[string]any, body string) (types.AgentConfig, error) {
	encoded, err := marshalYAML(spec)
	if err != nil {
		return types.AgentConfig{}, err
	}
	var agent types.AgentConfig
	if err := yaml.Unmarshal(encoded, &agent); err != nil {
		return types.AgentConfig{}, err
	}
	agent.Body = body
	return agent, nil
}

// decodeInto round-trips a generic value into a typed field. Used for the flow
// binding, whose sub-fields have custom unmarshalers (InputSpec accepts both a
// map and a list) that only run through yaml.
func decodeInto(value any, target any) error {
	if value == nil {
		return nil
	}
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(encoded, target); err != nil {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Spec builders for teams
// ---------------------------------------------------------------------------

// buildTeam assembles a team's generic document, decodes it into the typed
// struct, and returns both the struct (for the extra checks) and the bytes to
// write.
func buildTeam(
	req CreateTeamRequest,
	teamID string,
	existing types.Team,
	exists bool,
) (types.Team, []byte, error) {
	base := map[string]any{}
	switch {
	case exists:
		document, err := teamDocumentOf(existing)
		if err != nil {
			return types.Team{}, nil, err
		}
		base = document
	case req.Template != "":
		tmpl, err := LoadTemplate(req.Template)
		if err != nil {
			return types.Team{}, nil, err
		}
		if tmpl.Kind != KindTeam {
			return types.Team{}, nil, fmt.Errorf("template %q is not a team template", req.Template)
		}
		base = deepCopyMap(tmpl.Document)
		// A team template's calls are PLACEHOLDERS — they name a generic agent
		// that the caller has not created. Carrying them forward would do two
		// bad things at once: the writer would create a stray "generic" agent
		// nobody asked for, and the team would gain a call the spec never
		// wrote. The template contributes structure (state tuning, output
		// shape), never calls.
		delete(base, "calls")
		delete(base, "output")
		delete(base, "outputs")
	}

	spec, err := sanitizeTeamSpec(req.Spec)
	if err != nil {
		return types.Team{}, nil, err
	}
	merged := mergeTeamSpec(base, spec)
	merged["id"] = teamID

	team, err := decodeTeam(merged)
	if err != nil {
		return types.Team{}, nil, fmt.Errorf("team %q: %w", teamID, err)
	}

	data, err := renderTeam(team)
	if err != nil {
		return types.Team{}, nil, err
	}
	return team, data, nil
}

// rawSpecCalls pulls the calls block out of a team spec as a generic map,
// before any normalization. inlineAgentSpecs works off this rather than the
// merged team so it only ever sees what the caller actually wrote.
func rawSpecCalls(spec map[string]any) (map[string]any, error) {
	raw, ok := spec["calls"]
	if !ok || raw == nil {
		return nil, nil
	}
	calls, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("spec.calls must be a mapping of call name to call")
	}
	return calls, nil
}

// sanitizeTeamSpec keeps the team fields and converts the call shorthand into
// the document shape.
func sanitizeTeamSpec(spec map[string]any) (map[string]any, error) {
	cleaned := map[string]any{}
	for key, value := range spec {
		switch key {
		case "bind", "bind_key", "agent_specs", "template", "name", "id":
			continue
		default:
			cleaned[key] = deepCopyValue(value)
		}
	}

	rawCalls, ok := spec["calls"]
	if !ok || rawCalls == nil {
		return cleaned, nil
	}
	calls, ok := rawCalls.(map[string]any)
	if !ok {
		return nil, errors.New("spec.calls must be a mapping of call name to call")
	}

	normed := make(map[string]any, len(calls))
	for _, name := range sortedKeys(calls) {
		raw := calls[name]
		if raw == nil {
			return nil, fmt.Errorf("spec.calls.%s must be a mapping", name)
		}
		call, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("spec.calls.%s must be a mapping", name)
		}
		entry := deepCopyMap(call)
		// The call map key IS the call ID (Team.Normalize fills it, and
		// Team.Validate then requires them to agree), so the writer sets it
		// explicitly rather than relying on either.
		entry["id"] = name
		if _, ok := entry["type"]; !ok {
			entry["type"] = string(types.CallAgent)
		}
		normed[name] = entry
	}
	cleaned["calls"] = normed
	return cleaned, nil
}

func teamDocumentOf(team types.Team) (map[string]any, error) {
	encoded, err := marshalYAML(team)
	if err != nil {
		return nil, fmt.Errorf("encode team %q: %w", team.ID, err)
	}
	document := map[string]any{}
	if err := yaml.Unmarshal(encoded, &document); err != nil {
		return nil, fmt.Errorf("decode team %q: %w", team.ID, err)
	}
	return document, nil
}

func decodeTeam(document map[string]any) (types.Team, error) {
	encoded, err := marshalYAML(document)
	if err != nil {
		return types.Team{}, err
	}
	var team types.Team
	if err := yaml.Unmarshal(encoded, &team); err != nil {
		return types.Team{}, err
	}
	team.Normalize()
	return team, nil
}

// ---------------------------------------------------------------------------
// Filesystem
// ---------------------------------------------------------------------------

// configEntries are the top-level members of a config tree. Copying only these
// keeps a staging tree from inheriting session data, logs or anything else a
// caller parked next to the definitions.
var configEntries = []string{"flows", "teams", "agents", "skills", "rules", "settings.json", "models.json"}

// copyConfigTree copies the definitions out of src into a fresh dst.
//
// A whole-tree copy is what lets validation run against a candidate tree
// instead of the live one. It is affordable because config trees are small —
// definitions, not data: even a heavily customised tree is a few hundred KB,
// while the alternative (validating in place) would mean a concurrent
// LoadDefinitions could observe a partially written tree and reload the running
// engine into garbage.
//
// Missing entries are skipped rather than reported: a fresh tree legitimately
// has no skills/ or rules/ directory, and the loader treats absent as empty.
func copyConfigTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, name := range configEntries {
		source := filepath.Join(src, name)
		info, err := os.Stat(source)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		target := filepath.Join(dst, name)
		if info.IsDir() {
			if err := copyTree(source, target); err != nil {
				return err
			}
			continue
		}
		if err := copyFile(source, target, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)

		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !entry.Type().IsRegular() {
			// Symlinks and devices are not definitions; copying them would make
			// the staging tree an incomplete mirror the loader might follow into
			// the live tree.
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, mode fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// stagingPath maps an absolute path inside the real tree to the same location
// inside the staging copy.
//
// The staging root is deliberately named <root>/.heron-staging-<n>, i.e. a
// sibling of flows/ teams/ agents/ INSIDE the config root, and the flow path is
// mapped by relative position. That matters because the loader derives the
// config root from the flow path by looking for a "flows" directory with a
// sibling teams/ or agents/ (config.ConfigRootForFlow). A staging tree rooted
// anywhere else, or one whose flow lived at a different depth, would make the
// loader read the REAL teams/ and agents/ while validating the STAGED flow —
// silently validating the wrong thing.
func stagingPath(staging, root, absolute string) (string, error) {
	rel, err := filepath.Rel(root, absolute)
	if err != nil {
		return "", fmt.Errorf("locate %s under %s: %w", absolute, root, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("flow path %s is outside config root %s", absolute, root)
	}
	return filepath.Join(staging, rel), nil
}

// commitPlan writes every planned file into the real tree in commit order and
// returns the ops that landed, for rollback.
//
// Two properties are load-bearing:
//
//  1. Optimistic concurrency. Each file is re-read and compared byte for byte
//     against what step 1 captured. If anything changed, another Define call or
//     a human edit got there first and this apply aborts — merging on top of a
//     stale baseline would silently discard their change, and the plan's
//     rollback image would restore a version that was already outdated.
//
//  2. Commit order, from InCommitOrder: referenced definitions (agents, teams)
//     before referencing ones (the flow). Multi-file commit is not one atomic
//     syscall, so the order IS the guarantee — if the process dies midway, the
//     tree is left in a state that still loads.
func commitPlan(root string, plan *WritePlan) ([]WriteOp, error) {
	var committed []WriteOp

	for _, op := range plan.InCommitOrder() {
		target, err := safeJoin(root, op.Path)
		if err != nil {
			return committed, err
		}

		current, readErr := os.ReadFile(target)
		switch {
		case readErr == nil && !op.Existed:
			return committed, fmt.Errorf(
				"config changed concurrently: %s appeared since the write plan was built",
				op.Path,
			)
		case readErr == nil && !bytesEqual(current, op.Original):
			return committed, fmt.Errorf(
				"config changed concurrently: %s was modified since the write plan was built",
				op.Path,
			)
		case readErr != nil && !os.IsNotExist(readErr):
			return committed, fmt.Errorf("re-read %s: %w", op.Path, readErr)
		case readErr != nil && op.Existed:
			return committed, fmt.Errorf(
				"config changed concurrently: %s was removed since the write plan was built",
				op.Path,
			)
		}

		if op.CreatesDir {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return committed, fmt.Errorf("create directory for %s: %w", op.Path, err)
			}
		}
		if err := writeFileAtomic(target, op.Data); err != nil {
			return committed, fmt.Errorf("write %s: %w", op.Path, err)
		}
		committed = append(committed, op)
	}
	return committed, nil
}

// captureOriginals reads the current bytes of every planned file.
//
// The bytes serve three jobs at once, which is why they are read once, early:
// the rollback image, the compare-and-swap baseline the commit re-reads against,
// and — for upserts — the merge baseline, which the plan builder already
// consumed. By commit time the tree may no longer hold the original content, so
// a rollback that re-read instead of restoring these bytes would put the
// concurrent edit back as if it were the original.
func (p *WritePlan) captureOriginals(root string) error {
	for i := range p.Ops {
		op := &p.Ops[i]
		target, err := safeJoin(root, op.Path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(target)
		switch {
		case err == nil:
			op.Original = data
			op.Existed = true
		case os.IsNotExist(err):
			op.Original = nil
			op.Existed = false
		default:
			return fmt.Errorf("read %s: %w", op.Path, err)
		}

		if op.Mode == WriteReplace && !op.Existed {
			return fmt.Errorf(
				"write plan expects %s to exist but it is missing: the config tree changed since the definitions were loaded",
				op.Path,
			)
		}
	}
	return nil
}

// applyTo writes every op into treeRoot (a staging copy) in commit order.
//
// No concurrency check here on purpose: the staging tree is private to this
// apply, so the only writer that could race it is this function. The check
// happens once, in commitPlan, against the tree that actually matters.
func (p *WritePlan) applyTo(treeRoot string) error {
	for _, op := range p.InCommitOrder() {
		target, err := safeJoin(treeRoot, op.Path)
		if err != nil {
			return err
		}
		if op.CreatesDir {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create directory for %s: %w", op.Path, err)
			}
		}
		if err := writeFileAtomic(target, op.Data); err != nil {
			return fmt.Errorf("write %s: %w", op.Path, err)
		}
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
