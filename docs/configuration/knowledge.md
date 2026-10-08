# Knowledge Configuration

Knowledge entries are reference documents an agent **searches for itself**.
The engine does not index them, does not inject them, and does not summarise
them into the prompt. It tells the agent where the knowledge directories are,
and the agent finds what it needs with `Grep`, `Glob` and `Read`.

## How knowledge reaches the agent

There is one layer, and it is a pointer:

| What | Content | When it enters the prompt |
|-------|---------|---------------------------|
| **Pointer block** | the readable knowledge directories + "search them with Grep/Glob/Read" | Every turn, for any agent that has a readable directory |
| **Entry** | the file itself | Only when the agent greps for it and reads it |

The pointer block is **fixed**: it names directories, never entries, so its
size does not grow with the knowledge base and its text does not change from
turn to turn. That is deliberate — see the design note in
`docs/skill-progressive-disclosure.md` §3.4.

### Why not pre-indexed retrieval

An earlier version kept a resident metadata index and matched entries against
each turn's query with in-memory substring search, injecting one summary line
per hit. It was removed. It was strictly weaker than the `Grep` the agent
already has (metadata only: no regex, no line numbers, no body), it grew the
prompt with the corpus, and it inherited an index that went stale the moment a
file changed.

The current approach follows Anthropic's finding on Claude Code: *"agentic
search generally works better. It is also simpler and doesn't have the same
issues around security, privacy, staleness, and reliability."*

### What the agent needs to read knowledge

Nothing special. `Read`, `Grep` and `Glob` — declared like any other builtin:

```yaml
tools:
  builtin:
    - Read
    - Grep
    - Glob
```

There is no `ReadKnowledge` tool any more, and no per-entry id to pass. An
agent reads a knowledge file by its path, exactly as it reads any other file.

### What each agent can see

Visibility is a property of **where a file lives**, not of its frontmatter.
The engine's path filter enforces it on `Read`/`Grep`/`Glob`/`Write`, so an
agent cannot reach another owner's tree even by naming it directly.

| Location | Readable by |
|---|---|
| `.agents/knowledge/**` | every agent |
| `.agents/agents/<id>/knowledge/**` | agent `<id>` only |
| `.agents/teams/<id>/knowledge/**` | team `<id>`'s agents only |

The pointer block only names directories the calling agent may actually read:
a private tree is advertised only when it holds at least one loadable entry, so
the agent is never told to search a place that will return nothing.

## Structure

Knowledge files are Markdown with YAML frontmatter:

```yaml
---
id: seo-writing
title: SEO 写作规范
summary: 优化标题与结构时使用；覆盖标题长度、关键词位置与段落节奏。
keys: ["SEO", "博客", "写作", "标题优化"]
scope:
  type: all
---

# SEO Writing Guide

## Title Optimization
- Title length: 20-70 characters
- Include primary keyword in first 30%
- Use numbers and power words

## Content Structure
- Paragraphs: 2-4 sentences max
- Use H2/H3 for section breaks
- Include keyword in first 100 words after first H2
```

## Fields

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Entry identifier. Defaults to the filename stem. Used by `heron knowledge` for dedup and by you for naming — it is **not** a lookup key at run time |
| `title` | string | Human-readable name, shown at the top of the file |
| `summary` | string | One-line description of when the entry applies. Written into `index.md` and used by the learn path's dedup. Derived from the first prose paragraph when omitted |
| `keys` | array | Extra keywords, for the human reading the index and for learn-path dedup |
| `scope` | object | Visibility claim. Must agree with the file's location — see below |
| `status` | string | `active` (default), `deprecated`, `archived`. Only active entries load |
| `expires_at` | string | RFC3339 timestamp. The only thing `heron knowledge gc` archives on |

### scope must match location

Because visibility is decided by path, a frontmatter scope that contradicts
where the file lives is **rejected at startup**, with an error naming the file,
quoting what the frontmatter says, and stating what the location implies:

| `scope.type` | Valid locations |
|---|---|
| `all` / `flow` / absent | any (in a private directory it means "private to the owner") |
| `team` (+ `teams: [t]`) | `.agents/teams/t/knowledge/` only |
| `agent` (+ `agents: [a]`) | `.agents/agents/a/knowledge/` only |

An entry claiming `agent` scope with an empty `agents:` list is also rejected:
no path can express "private to nobody", so the claim is unenforceable.

## File Location

### Global knowledge

```
.agents/knowledge/
├── seo-guide.md
└── writing-style.md
```

### Agent-private knowledge

```
.agents/agents/researcher/
├── AGENT.md
└── knowledge/
    ├── index.md
    └── research-methods.md
```

Every knowledge directory may contain an `index.md` navigation file. It is not
itself treated as a knowledge entry and is skipped by the loader.

## Authoring for agentic search

Because the agent greps rather than reading summaries:

- **Write concrete, greppable text.** A term the agent will search for should
  appear verbatim in the body. This is the opposite of SEO-style variation.
- **Keep one topic per file**, with a descriptive filename and a `#` heading
  that names it — the heading is what `Grep` matches first.
- **Use `summary` for the human reading `index.md`** and for learn-path dedup;
  it is no longer the agent's selection signal.
- **Prefer a stable path.** The agent has no id→path map; it finds files by
  directory and name, so renaming a file is a breaking change for any guidance
  that referenced it.

## Lifecycle

```bash
heron knowledge <session-id>   # distill a session into knowledge entries
heron knowledge                # learn from every session
heron knowledge gc             # archive entries whose expires_at has passed
```

`gc` used to also archive entries that were old and had never been retrieved.
It cannot any more: hit statistics were recorded by the retrieval path that has
been removed, and nothing else observes the agent reading a knowledge file. An
entry with no `expires_at` is now kept indefinitely — say so with `expires_at`
if you want it collected.
