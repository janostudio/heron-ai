package agent

import (
	"path"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

// pathTarget is one path a tool call is about to touch, with the direction that
// decides which kind of contract it can trigger.
//
// Direction matters because the two halves of a directory contract read
// differently: "you are editing this repo" (read side) is a different statement
// from "you are writing into this directory" (write side), and a rule author
// writes a pattern expecting one of them.
type pathTarget struct {
	Path string
	Kind string // write | read | search
}

// callPathTargets derives the paths a tool call will touch from its arguments.
//
// The workspace layer also reports paths, but only after the call succeeds, and
// it reports nothing for a command it ran in a shell. Deriving them from the
// arguments is what makes "I am about to write into docs/" visible even when the
// write turns out to fail, and it is the only way a shell command can be seen at
// all. On the read side the derived path and the reported one agree; on the
// write side the derived one is the only one.
func callPathTargets(call types.ToolCall) []pathTarget {
	name := call.Name
	args := call.Arguments

	switch name {
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		return writeTargets(argPath(args))
	case "Read", "NotebookRead":
		return oneTarget(argPath(args), "read")
	case "Grep", "Glob":
		if target := argPath(args); target != "" {
			return oneTarget(target, "search")
		}
		// A search with no path runs at the workspace root: every directory
		// contract applies, which is the honest answer for "search everywhere".
		return []pathTarget{{Path: ".", Kind: "search"}}
	case "Bash":
		return bashTargets(argString(args, "command"))
	default:
		return nil
	}
}

// opPathTargets turns the workspace layer's report of what a tool did into the
// same shape. It is the authoritative half: those paths are already resolved
// against the workspace root, and they cover the tools whose argument names this
// package does not know.
func opPathTargets(ops []types.WorkspaceOperation) []pathTarget {
	if len(ops) == 0 {
		return nil
	}
	out := make([]pathTarget, 0, len(ops)*2)
	for _, op := range ops {
		if op.Path == "" {
			continue
		}
		switch op.Kind {
		case "write":
			out = append(out, writeTargets(op.Path)...)
		case "search":
			out = append(out, oneTarget(op.Path, "search")...)
		default:
			out = append(out, oneTarget(op.Path, "read")...)
		}
	}
	return out
}

func oneTarget(value, kind string) []pathTarget {
	value = normalizeRulePath(value)
	if value == "" {
		return nil
	}
	return []pathTarget{{Path: value, Kind: kind}}
}

// writeTargets keeps a write target as a path, and adds its parent directory for
// patterns like "docs/**" that are written as directories. A pattern that names
// the file exactly ("docs/INDEX.md") still matches the first form.
func writeTargets(value string) []pathTarget {
	value = normalizeRulePath(value)
	if value == "" {
		return nil
	}
	targets := []pathTarget{{Path: value, Kind: "write"}}
	if parent := path.Dir(value); parent != "." && parent != "/" {
		targets = append(targets, pathTarget{Path: parent, Kind: "write"})
	}
	return targets
}

// bashTargets extracts the paths a shell command names, and stops at the cases
// it cannot read reliably.
//
// This is deliberately narrow. A command is a program, and guessing what it
// writes would be worse than admitting the gap: a rule that fires on a wrong
// guess teaches the model to ignore rule notes. So only three shapes are read —
// redirections ("> path", ">> path"), "tee [-a] path", and the working-directory
// forms ("-C dir", "cd dir") — and anything with a variable, a glob or a command
// substitution in the target is dropped.
func bashTargets(command string) []pathTarget {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	tokens := shellTokens(command)
	var targets []pathTarget
	addWrite := func(candidate string) {
		if !shellPathIsStatic(candidate) {
			return
		}
		targets = append(targets, writeTargets(candidate)...)
	}
	addRead := func(candidate string) {
		if !shellPathIsStatic(candidate) {
			return
		}
		targets = append(targets, oneTarget(candidate, "search")...)
	}

	for index, token := range tokens {
		switch {
		case strings.HasPrefix(token, ">>"), strings.HasPrefix(token, ">"):
			target := strings.TrimPrefix(strings.TrimPrefix(token, ">>"), ">")
			if target == "" && index+1 < len(tokens) {
				target = tokens[index+1]
			}
			addWrite(target)
		case token == "tee":
			next := ""
			if index+1 < len(tokens) {
				next = tokens[index+1]
			}
			if next == "-a" || next == "--append" {
				if index+2 < len(tokens) {
					addWrite(tokens[index+2])
				}
				continue
			}
			addWrite(next)
		case token == "-C" || token == "--directory":
			if index+1 < len(tokens) {
				addRead(tokens[index+1])
			}
		case token == "cd":
			if index+1 < len(tokens) {
				addRead(tokens[index+1])
			}
		}
	}
	return targets
}

// shellTokens splits a command into the tokens the shapes above are made of.
// Quotes and backticks are dropped as separators; the point is finding path
// arguments, not faithfully re-parsing a shell grammar.
func shellTokens(command string) []string {
	fields := strings.FieldsFunc(command, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r', ';', '&', '|', '(', ')', '"', '\'', '`':
			return true
		}
		return false
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" {
			out = append(out, field)
		}
	}
	return out
}

// shellPathIsStatic rejects the targets that cannot be known before the command
// runs: a variable, a command substitution, or a glob. Injecting a contract for
// a path the command may never write is noise the model learns to skip.
func shellPathIsStatic(token string) bool {
	if token == "" {
		return false
	}
	return !strings.ContainsAny(token, "$*?[]{}~")
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	value, ok := args[key]
	if !ok {
		return ""
	}
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

// argPath reads the path-like argument of a builtin tool. The key differs per
// tool and there is no single convention to fix: Write has "file", Glob has
// "path", Grep has "path", and older definitions send "file_path".
func argPath(args map[string]any) string {
	for _, key := range []string{"file", "file_path", "path", "dir", "directory"} {
		if value := argString(args, key); value != "" {
			return value
		}
	}
	return ""
}
