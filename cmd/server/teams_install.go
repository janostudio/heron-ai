package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/heron-ai/heron-engine/internal/storage"
	"github.com/heron-ai/heron-engine/internal/teamspkg"
)

// Decompression limits. A shared package is text definitions plus a few small
// scripts, so anything near these sizes is either a mistake or a decompression
// bomb, and both should stop before the disk fills.
const (
	maxPackageFileSize  = 10 << 20  // 10 MiB per file
	maxPackageTotalSize = 100 << 20 // 100 MiB after decompression
)

// scriptPreviewLines bounds how much of a script a review shows: enough to
// recognize what it does, not enough to hide a payload behind pages of noise.
const scriptPreviewLines = 20

// installOptions is the CLI's view of the teams add flags.
type installOptions struct {
	DryRun bool
	Yes    bool
	Scope  string
	// Refresh only applies to remote sources; it forces a re-download instead
	// of reusing the cache entry for this URL and ref.
	Refresh bool
}

// trimmedAgent records one Agent whose builtin tools were cut by the package's
// own sandbox declaration.
type trimmedAgent struct {
	Agent string
	Tools []string
}

// installResult reports what an install changed, for the closing summary.
type installResult struct {
	Manifest        *teamspkg.Manifest
	Scope           string
	Version         string
	TargetDir       string
	Trimmed         []trimmedAgent
	ScriptsReviewed int
	MissingModels   []string
	Teams           []string
	Agents          []string
	Skills          []string
	RuleCount       int
}

// installTeamPackage runs the whole install pipeline: acquire, validate,
// sanitize, write, index.
//
// Every step that can reject the package runs before the first byte reaches the
// target directory, because a half-written _teams/<scope>/ would list as
// installed while being unusable.
func installTeamPackage(root, source string, opts installOptions, stdout, stderr io.Writer, stdin io.Reader) (*installResult, error) {
	srcDir, cleanup, err := acquirePackageSource(source, opts.Refresh, stdout)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	manifestData, err := readPackageManifest(srcDir)
	if err != nil {
		return nil, err
	}
	// The manifest is validated before the tree is read so a package aimed at a
	// newer engine fails on its apiVersion, not on a downstream symptom.
	manifest, err := teamspkg.LoadManifest(manifestData)
	if err != nil {
		return nil, err
	}

	tree, err := teamspkg.Collect(storage.NewFileStore(""), srcDir)
	if err != nil {
		return nil, err
	}
	check := teamspkg.Check(manifest, tree)
	// Errors go into the returned error and warnings are printed here, so each
	// message appears exactly once in the caller's output.
	for _, p := range check.Problems {
		if p.Severity != teamspkg.SeverityError {
			_, _ = fmt.Fprintf(stderr, "warning: %s\n", p.Message)
		}
	}
	if check.HasErrors() {
		return nil, fmt.Errorf("package %s failed validation:\n  %s", manifest.Name, joinErrorProblems(check.Problems))
	}

	scope := strings.TrimSpace(opts.Scope)
	if scope == "" {
		scope = manifest.Name
	}
	if !teamspkg.ValidName(scope) {
		return nil, fmt.Errorf("invalid install scope %q: must start with a letter or digit and contain only [a-z0-9-]", scope)
	}
	targetDir := filepath.Join(root, ".agents", "_teams", scope)

	replacing, err := checkScopeConflict(targetDir, scope, manifest)
	if err != nil {
		return nil, err
	}

	trimmed := trimPackageTools(tree, manifest)

	// A dry run never prompts: a preview that blocks on stdin is unusable in
	// scripts, and it writes nothing, so approving scripts it will not install
	// would be a pointless gate. The script list is printed instead.
	reviewOpts := opts
	reviewOpts.Yes = reviewOpts.Yes || opts.DryRun
	scripts, err := reviewPackageScripts(srcDir, tree, manifest, reviewOpts, stdout, stdin)
	if err != nil {
		return nil, err
	}

	result := &installResult{
		Manifest:        manifest,
		Scope:           scope,
		Version:         manifest.Version,
		TargetDir:       targetDir,
		Trimmed:         trimmed,
		ScriptsReviewed: len(scripts),
		// Recomputed after trimming so the list matches the models the
		// installed Agents will actually ask for.
		MissingModels: missingModels(root, collectModels(tree)),
		Teams:         sortedKeys(tree.Teams),
		Agents:        sortedKeys(tree.Agents),
		Skills:        sortedKeys(tree.Skills),
		RuleCount:     len(tree.Rules),
	}

	if opts.DryRun {
		writeInstallDryRun(stdout, manifest, result, scripts)
		return result, nil
	}

	if err := writeInstalledPackage(srcDir, targetDir, tree, trimmed, replacing); err != nil {
		return nil, err
	}

	fileHashes, err := hashDirFiles(targetDir)
	if err != nil {
		return nil, err
	}
	entry := installedTeam{
		Version:      manifest.Version,
		Source:       source,
		InstalledAt:  time.Now().UTC().Format(time.RFC3339),
		ManifestHash: hashBytes(manifestData),
		Files:        fileHashes,
		Scripts:      hashScripts(targetDir, scripts),
	}
	// A remote install records the address it came from, because the manifest
	// only names the author's own source and the cache directory is opaque.
	if spec, err := resolveRemoteSource(source); err == nil && isRemoteSource(source) {
		entry.Remote = spec.URL
	}
	if err := updateTeamsIndex(root, scope, entry); err != nil {
		return nil, err
	}
	return result, nil
}

// acquirePackageSource resolves a local directory or tar.gz into a directory
// and returns a cleanup func for any temporary state it created.
func acquirePackageSource(source string, refresh bool, stdout io.Writer) (string, func(), error) {
	noop := func() {}
	if isRemoteSource(source) {
		base, err := defaultFetchCacheBase()
		if err != nil {
			return "", noop, err
		}
		dir, err := fetchRemotePackage(base, source, refresh, nil, stdout)
		if err != nil {
			return "", noop, err
		}
		return dir, noop, nil
	}
	fi, err := os.Stat(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", noop, fmt.Errorf("source not found: %s", source)
		}
		return "", noop, err
	}
	if fi.IsDir() {
		return source, noop, nil
	}
	name := strings.ToLower(source)
	if !strings.HasSuffix(name, ".tar.gz") && !strings.HasSuffix(name, ".tgz") {
		return "", noop, fmt.Errorf("source must be a directory or .tar.gz archive: %s", source)
	}
	dir, err := os.MkdirTemp("", "heron-team-*")
	if err != nil {
		return "", noop, err
	}
	if err := extractTarGz(source, dir); err != nil {
		_ = os.RemoveAll(dir)
		return "", noop, err
	}
	return unwrapPackageRoot(dir)
}

// unwrapPackageRoot steps into the single top-level directory an export
// produces (<manifest.name>/...). Any other shape is returned as-is; the
// manifest lookup that follows decides whether it is a package.
func unwrapPackageRoot(dir string) (string, func(), error) {
	cleanup := func() { _ = os.RemoveAll(dir) }
	entries, err := os.ReadDir(dir)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	visible := make([]os.DirEntry, 0, len(entries))
	for _, e := range entries {
		// macOS tar tools prepend these; they are never the package.
		if e.Name() == "__MACOSX" || e.Name() == ".DS_Store" {
			continue
		}
		visible = append(visible, e)
	}
	if len(visible) == 1 && visible[0].IsDir() {
		return filepath.Join(dir, visible[0].Name()), cleanup, nil
	}
	return dir, cleanup, nil
}

// extractTarGz unpacks an archive under dst, rejecting anything that could
// escape it.
//
// Links are refused rather than resolved: a package that links outside itself
// can install a file the user never reviewed, and links have no legitimate use
// in a definitions package.
func extractTarGz(archive, dst string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read %s: %w", archive, err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", archive, err)
		}

		name := filepath.Clean(filepath.FromSlash(hdr.Name))
		if name == "." || name == string(filepath.Separator) {
			continue
		}
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("archive entry escapes the package: %s", hdr.Name)
		}
		target := filepath.Join(dst, name)
		if !strings.HasPrefix(target, dst+string(filepath.Separator)) {
			return fmt.Errorf("archive entry escapes the package: %s", hdr.Name)
		}

		switch hdr.Typeflag {
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("archive contains a link, which a package may not ship: %s", hdr.Name)
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if hdr.Size > maxPackageFileSize {
				return fmt.Errorf("archive entry %s is %d bytes, over the %d byte limit", hdr.Name, hdr.Size, maxPackageFileSize)
			}
			total += hdr.Size
			if total > maxPackageTotalSize {
				return fmt.Errorf("archive expands to over %d bytes", maxPackageTotalSize)
			}
			if err := writeExtractedFile(target, tr, hdr.Size); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive contains an unsupported entry type %q: %s", hdr.Typeflag, hdr.Name)
		}
	}
	return nil
}

func writeExtractedFile(target string, r io.Reader, size int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	// CopyN to the vetted size: the limit above is only meaningful if nothing
	// reads past the header that was checked.
	if _, err := io.CopyN(out, r, size); err != nil {
		_ = out.Close()
		return fmt.Errorf("extract %s: %w", target, err)
	}
	return out.Close()
}

// readPackageManifest returns the raw manifest bytes, which are hashed into the
// index so a later remove can tell a hand-edited package from an installed one.
func readPackageManifest(dir string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(dir, teamManifestFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("missing %s in %s", teamManifestFile, dir)
		}
		return nil, err
	}
	return data, nil
}

// checkScopeConflict applies the policy for an occupied scope: the same package
// again is an idempotent overwrite, anything else is refused.
//
// Refusing a different version is deliberate. Overwriting would silently change
// what the user's flows run, and the package format has no upgrade semantics.
func checkScopeConflict(targetDir, scope string, manifest *teamspkg.Manifest) (bool, error) {
	if _, err := os.Stat(targetDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	existing, err := readPackageManifest(targetDir)
	if err != nil {
		return false, fmt.Errorf("scope @%s already exists without a readable %s: %w", scope, teamManifestFile, err)
	}
	prev, err := teamspkg.LoadManifest(existing)
	if err != nil {
		return false, fmt.Errorf("scope @%s already exists with an invalid manifest: %w", scope, err)
	}
	if prev.Name != manifest.Name {
		return false, fmt.Errorf(
			"scope @%s already holds package %q; use --as <scope> to install %q alongside it",
			scope, prev.Name, manifest.Name,
		)
	}
	if prev.Version != manifest.Version {
		return false, fmt.Errorf(
			"team package %s %s is already installed at @%s; run `heron teams remove @%s` before installing %s",
			prev.Name, prev.Version, scope, scope, manifest.Version,
		)
	}
	return true, nil
}

// trimPackageTools intersects every Agent's builtin tools with sandbox.tools.
//
// It happens at install time rather than at runtime so the file on disk matches
// what actually runs: an Agent declaring Bash it can never use is a trap. An
// empty sandbox.tools means the package declared no ceiling.
func trimPackageTools(tree *teamspkg.Tree, manifest *teamspkg.Manifest) []trimmedAgent {
	if len(manifest.Sandbox.Tools) == 0 {
		return nil
	}
	allowed := make(map[string]struct{}, len(manifest.Sandbox.Tools))
	for _, tool := range manifest.Sandbox.Tools {
		allowed[tool] = struct{}{}
	}

	var out []trimmedAgent
	for _, name := range sortedKeys(tree.Agents) {
		agent := tree.Agents[name]
		kept := make([]string, 0, len(agent.Tools.Builtin))
		var removed []string
		for _, tool := range agent.Tools.Builtin {
			if _, ok := allowed[tool]; ok {
				kept = append(kept, tool)
				continue
			}
			removed = append(removed, tool)
		}
		if len(removed) == 0 {
			continue
		}
		agent.Tools.Builtin = kept
		tree.Agents[name] = agent
		out = append(out, trimmedAgent{Agent: name, Tools: removed})
	}
	return out
}

// packageScript is one script as found on disk, keyed by its package-relative
// path so the same key works after installation.
type packageScript struct {
	Path string
	Data []byte
}

// reviewPackageScripts applies sandbox.scripts to the scripts the package
// ships.
//
// Only scripts declared by shipped Skills count: an undeclared script is never
// executed, so failing an install over it would reject packages for files their
// Team cannot reach.
func reviewPackageScripts(srcDir string, tree *teamspkg.Tree, manifest *teamspkg.Manifest, opts installOptions, stdout io.Writer, stdin io.Reader) ([]packageScript, error) {
	scripts, err := collectPackageScripts(srcDir, tree)
	if err != nil {
		return nil, err
	}
	if len(scripts) == 0 {
		return nil, nil
	}

	switch manifest.Sandbox.Scripts {
	case teamspkg.ScriptsDeny:
		return nil, fmt.Errorf(
			"package %s ships scripts but sandbox.scripts is deny: %s",
			manifest.Name, strings.Join(scriptPaths(scripts), ", "),
		)
	case teamspkg.ScriptsAllow:
		return scripts, nil
	}

	if opts.Yes {
		return scripts, nil
	}
	// Both checks matter: the CLI reaches here with no terminal in a piped run
	// whether it passes nil or an exhausted reader, and neither may silently
	// count as approval.
	if stdin == nil || readerAtEOF(stdin) {
		return nil, errors.New("package scripts need review but no terminal is available; re-run with --yes to accept them")
	}
	_, _ = fmt.Fprintf(stdout, "Package %s ships %d script(s):\n", manifest.Name, len(scripts))
	for _, s := range scripts {
		_, _ = fmt.Fprintf(stdout, "\n--- %s\n", s.Path)
		writeScriptPreview(stdout, s.Data)
	}
	_, _ = fmt.Fprintf(stdout, "\nInstall these scripts? [y/N] ")
	if !confirm(stdin) {
		return nil, errors.New("install cancelled: package scripts were not approved")
	}
	return scripts, nil
}

// collectPackageScripts resolves the declared script paths of every Skill in
// the tree.
func collectPackageScripts(srcDir string, tree *teamspkg.Tree) ([]packageScript, error) {
	seen := map[string]struct{}{}
	var out []packageScript
	for _, skillName := range sortedKeys(tree.Skills) {
		for _, rel := range tree.Scripts[skillName] {
			clean := filepath.Clean(filepath.FromSlash(rel))
			if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("skill %q declares a script outside its directory: %s", skillName, rel)
			}
			pkgRel := filepath.ToSlash(filepath.Join("skills", skillName, clean))
			if _, ok := seen[pkgRel]; ok {
				continue
			}
			seen[pkgRel] = struct{}{}
			data, err := os.ReadFile(filepath.Join(srcDir, filepath.FromSlash(pkgRel)))
			if err != nil {
				return nil, fmt.Errorf("read script %s: %w", pkgRel, err)
			}
			out = append(out, packageScript{Path: pkgRel, Data: data})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// joinErrorProblems renders the error-level findings for a single error line.
func joinErrorProblems(problems []teamspkg.Problem) string {
	var msgs []string
	for _, p := range problems {
		if p.Severity == teamspkg.SeverityError {
			msgs = append(msgs, p.Message)
		}
	}
	return strings.Join(msgs, "\n  ")
}

func scriptPaths(scripts []packageScript) []string {
	paths := make([]string, 0, len(scripts))
	for _, s := range scripts {
		paths = append(paths, s.Path)
	}
	return paths
}

func writeScriptPreview(w io.Writer, data []byte) {
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for i, line := range lines {
		if i >= scriptPreviewLines {
			_, _ = fmt.Fprintf(w, "... (%d more lines)\n", len(lines)-scriptPreviewLines)
			break
		}
		_, _ = fmt.Fprintln(w, line)
	}
}

// confirm reads one line. Anything but y/yes is a no: a bare Enter must not
// approve running foreign code.
// readerAtEOF reports whether r holds no input, including the typed-nil case
// (a nil *strings.Reader stored in an io.Reader), which would otherwise panic
// in Read instead of failing the install.
func readerAtEOF(r io.Reader) bool {
	v := reflect.ValueOf(r)
	if !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil()) {
		return true
	}
	// A character device is a terminal: a human can type into it even though a
	// non-blocking read may currently see nothing pending.
	if f, ok := r.(interface{ Stat() (os.FileInfo, error) }); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return false
		}
	}
	return !hasPendingInput(r)
}

// hasPendingInput peeks one byte without consuming it, for readers that can
// tell us whether anything is waiting (bufio.Reader, strings.Reader).
func hasPendingInput(r io.Reader) bool {
	p, ok := r.(interface{ Peek(int) ([]byte, error) })
	if !ok {
		return true
	}
	_, err := p.Peek(1)
	return err == nil
}

func confirm(r io.Reader) bool {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// writeInstalledPackage copies the package into the scope directory.
//
// The copy is staged beside the target and renamed into place so an interrupt
// midway leaves the previous install intact instead of a half-copied tree that
// still carries a manifest and therefore lists as installed.
func writeInstalledPackage(srcDir, targetDir string, tree *teamspkg.Tree, trimmed []trimmedAgent, replacing bool) error {
	parent := filepath.Dir(targetDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, "."+filepath.Base(targetDir)+"-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if err := copyPackageTree(srcDir, staging); err != nil {
		return err
	}
	for _, t := range trimmed {
		if err := rewriteAgentTools(staging, t.Agent, tree.Agents[t.Agent].Tools.Builtin); err != nil {
			return err
		}
	}

	if replacing {
		if err := os.RemoveAll(targetDir); err != nil {
			return err
		}
	}
	if err := os.Rename(staging, targetDir); err != nil {
		return fmt.Errorf("move package into place: %w", err)
	}
	return nil
}

// copyPackageTree copies a package directory, skipping VCS metadata.
//
// It walks the source instead of writing the parsed tree because a package may
// carry files the loader does not model (READMEs, script fixtures), and
// dropping them would install a different package than the author published.
func copyPackageTree(srcDir, dstDir string) error {
	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".svn" || d.Name() == ".hg") {
			return filepath.SkipDir
		}
		// A link in a directory source would point at a file outside the
		// package, so it is refused here the same way the archive path does.
		if !d.IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("package contains a non-regular file: %s", rel)
		}
		target := filepath.Join(dstDir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// rewriteAgentTools replaces tools.builtin in an installed AGENT.md.
//
// It edits the parsed YAML node tree instead of round-tripping through
// types.AgentConfig: the config struct only knows the fields the engine reads
// today, so re-marshalling it would drop comments, key order and any field a
// newer package carries. Only the one key changes; everything else survives.
func rewriteAgentTools(root, agentName string, kept []string) error {
	path := filepath.Join(root, "agents", agentName, "AGENT.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("rewrite agent %q: %w", agentName, err)
	}
	front, body, err := splitFrontmatter(string(data))
	if err != nil {
		return fmt.Errorf("rewrite agent %q: %w", agentName, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(front), &doc); err != nil {
		return fmt.Errorf("rewrite agent %q: %w", agentName, err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("rewrite agent %q: frontmatter is not a mapping", agentName)
	}

	// Flow style reads better for the short tool lists agents carry, and an
	// empty list has to render as [] rather than null, which would read back as
	// "field absent".
	list := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Style: yaml.FlowStyle}
	for _, tool := range kept {
		list.Content = append(list.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: tool})
	}
	setMappingPath(doc.Content[0], list, "tools", "builtin")

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("rewrite agent %q: %w", agentName, err)
	}
	rendered := "---\n" + string(out) + "---\n" + strings.TrimLeft(body, "\n")
	return os.WriteFile(path, []byte(rendered), 0o644)
}

// splitFrontmatter returns the YAML between the leading --- delimiters and the
// markdown body after them. It reads the delimiters from the source rather than
// asking frontmatter.Parse, which keeps the body byte-identical.
func splitFrontmatter(text string) (string, string, error) {
	const delimiter = "---"

	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(normalized, delimiter) {
		return "", "", errors.New("frontmatter: file does not start with ---")
	}
	rest := normalized[len(delimiter):]
	end := strings.Index(rest, "\n"+delimiter)
	if end < 0 {
		return "", "", errors.New("frontmatter: closing --- not found")
	}
	return rest[:end], rest[end+len("\n"+delimiter):], nil
}

// setMappingPath sets value at the mapping path, creating intermediate
// mappings, so a package Agent that never declared tools still ends up with the
// trimmed list instead of keeping the original.
func setMappingPath(m *yaml.Node, value *yaml.Node, path ...string) {
	for i, key := range path {
		last := i == len(path)-1
		if last {
			setMappingKey(m, key, value)
			return
		}
		child := mappingValue(m, key)
		if child == nil || child.Kind != yaml.MappingNode {
			child = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			setMappingKey(m, key, child)
		}
		m = child
	}
}

func mappingValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setMappingKey(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, value)
}

// collectModels gathers the model ids the trimmed tree asks for.
//
// The tree's own Check.Models is computed before trimming; trimming only
// removes tools, so the ids are the same here, but taking them from the tree
// keeps the summary tied to what was written rather than to what was read.
func collectModels(tree *teamspkg.Tree) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, name := range sortedKeys(tree.Agents) {
		id := strings.TrimSpace(tree.Agents[name].Model.Model)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// missingModels reports which of the package's model ids are absent from the
// receiver's models.json.
//
// It is advisory only: §8 keeps model availability out of the install gate, so
// this only feeds the summary line that tells the user what to add.
func missingModels(root string, required []string) []string {
	if len(required) == 0 {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(root, ".agents", "models.json"))
	if err != nil {
		return append([]string(nil), required...)
	}
	var cfg ModelsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return append([]string(nil), required...)
	}
	have := make(map[string]struct{}, len(cfg.Models))
	for _, m := range cfg.Models {
		have[m.Name] = struct{}{}
	}
	var missing []string
	for _, id := range required {
		if _, ok := have[id]; !ok {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}

// hashDirFiles hashes every file under dir with paths relative to it.
func hashDirFiles(dir string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = hashBytes(data)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// hashScripts records each script's content hash so a package upgrade that
// changes a script is visible in the index.
func hashScripts(pkgDir string, scripts []packageScript) map[string]string {
	if len(scripts) == 0 {
		return nil
	}
	out := make(map[string]string, len(scripts))
	for _, s := range scripts {
		data, err := os.ReadFile(filepath.Join(pkgDir, filepath.FromSlash(s.Path)))
		if err != nil {
			// The file was read moments ago; a failure here is not worth
			// failing an otherwise complete install over.
			continue
		}
		out[s.Path] = hashBytes(data)
	}
	return out
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// writeInstallDryRun reports what a real install would produce.
func writeInstallDryRun(out io.Writer, manifest *teamspkg.Manifest, result *installResult, scripts []packageScript) {
	title := manifest.Name + " " + manifest.Version
	_, _ = fmt.Fprintf(out, "Applying team package: %s\n", title)
	if d := strings.TrimSpace(manifest.Description); d != "" {
		_, _ = fmt.Fprintf(out, "  description: %s\n", d)
	}
	_, _ = fmt.Fprintf(out, "  teams:    %d%s\n", len(result.Teams), formatNames(result.Teams))
	_, _ = fmt.Fprintf(out, "  agents:   %d%s\n", len(result.Agents), formatNames(result.Agents))
	_, _ = fmt.Fprintf(out, "  skills:   %d%s\n", len(result.Skills), formatNames(result.Skills))
	_, _ = fmt.Fprintf(out, "  rules:    %d\n", result.RuleCount)
	_, _ = fmt.Fprintf(out, "Target: .agents/_teams/%s/\n", result.Scope)
	for _, t := range result.Trimmed {
		_, _ = fmt.Fprintf(out, "  would trim tools: %s: %s\n", t.Agent, strings.Join(t.Tools, ", "))
	}
	if len(scripts) > 0 {
		_, _ = fmt.Fprintf(out, "  scripts: %d%s (%s)\n", len(scripts), formatNames(scriptPaths(scripts)), scriptPolicyNote(manifest.Sandbox.Scripts))
	}
	_, _ = fmt.Fprintln(out, "Dry run: nothing was written.")
}

func scriptPolicyNote(policy string) string {
	switch policy {
	case teamspkg.ScriptsAllow:
		return "sandbox.scripts is allow, installed without review"
	case teamspkg.ScriptsDeny:
		return "sandbox.scripts is deny, install would be refused"
	default:
		return "would be reviewed before install"
	}
}

// writeInstallSummary prints the post-install report plus the Flow snippet the
// user may paste. The Flow is never written: team bindings are the receiver's
// orchestration decision, not the package's.
func writeInstallSummary(out io.Writer, manifest *teamspkg.Manifest, result *installResult) {
	_, _ = fmt.Fprintf(out, "Installed team package: %s %s -> .agents/_teams/%s/\n", manifest.Name, result.Version, result.Scope)
	for _, t := range result.Trimmed {
		_, _ = fmt.Fprintf(out, "  trimmed tools: %s: %s\n", t.Agent, strings.Join(t.Tools, ", "))
	}
	if result.ScriptsReviewed > 0 {
		_, _ = fmt.Fprintf(out, "  scripts reviewed: %d\n", result.ScriptsReviewed)
	}
	if len(result.MissingModels) > 0 {
		_, _ = fmt.Fprintf(out, "  missing models: %s (add it to models.json to run)\n", strings.Join(result.MissingModels, ", "))
	}
	if len(result.Teams) == 0 {
		return
	}

	teamID := result.Teams[0]
	_, _ = fmt.Fprintln(out, "")
	_, _ = fmt.Fprintln(out, "Add it to a flow:")
	_, _ = fmt.Fprintln(out, "  # .agents/flows/default.yml")
	_, _ = fmt.Fprintln(out, "  teams:")
	_, _ = fmt.Fprintf(out, "    %s:\n", flowAlias(result.Scope))
	_, _ = fmt.Fprintf(out, "      team: %q\n", result.Scope+"/"+teamID)
	_, _ = fmt.Fprintln(out, "      inputs:")
	_, _ = fmt.Fprintln(out, "        user_message: true")
}

// flowAlias derives a Flow key from the scope, replacing the characters Flow
// keys are not written with.
func flowAlias(scope string) string {
	return strings.ReplaceAll(scope, "-", "_")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
