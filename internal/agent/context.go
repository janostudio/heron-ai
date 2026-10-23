package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/heron-ai/heron-engine/pkg/types"
)

var ErrContextLimit = errors.New("agent context exceeds hard limit")

// defaultSummaryTokenBudget bounds the compaction summary when the manager has
// no configured input capacity to derive one from. Small on purpose: it is a
// recap of what was dropped, not a replacement for the history.
const defaultSummaryTokenBudget = 4096

// ContextManager separates the complete Agent transcript from the bounded
// message list sent to the model. CanonicalMessages is never compacted;
// Messages returns the active model context.
type ContextManager interface {
	AddMessage(message types.Message) error
	Messages() []types.Message
	CanonicalMessages() []types.Message
	RestoreActive(messages []types.Message) error
	SetToolSchemas(schemas []types.JSONSchema)
	EstimateTokens() int
	NeedsCompaction() bool
	Compact(ctx context.Context) error
	CompactForce(ctx context.Context) error
	Reset() error
}

// ContextTokenEstimator is intentionally small so a provider-specific token
// counter can be injected without coupling the Agent runtime to one model
// implementation.
type ContextTokenEstimator interface {
	EstimateMessages(messages []types.Message) int
	EstimateTools(tools []types.JSONSchema) int
}

type MessageContextManager struct {
	mu            sync.RWMutex
	config        types.ContextConfig
	estimator     ContextTokenEstimator
	compactor     Compactor
	canonical     []types.Message
	active        []types.Message
	tools         []types.JSONSchema
	compactions   int
	microcompacts int
	compactHook   func(summary string, droppedCount int)
}

func NewContextManager(config types.ContextConfig) *MessageContextManager {
	return NewContextManagerWithEstimator(config, defaultContextTokenEstimator{})
}

func NewContextManagerWithEstimator(config types.ContextConfig, estimator ContextTokenEstimator) *MessageContextManager {
	return NewContextManagerWithCompactor(config, estimator, nil)
}

func NewContextManagerWithCompactor(config types.ContextConfig, estimator ContextTokenEstimator, compactor Compactor) *MessageContextManager {
	if estimator == nil {
		estimator = defaultContextTokenEstimator{}
	}
	if compactor == nil {
		compactor = mechanicalCompactor{}
	}
	return &MessageContextManager{
		config:    config.WithDefaults(),
		estimator: estimator,
		compactor: compactor,
	}
}

// AddMessage appends one message and compacts when the threshold is crossed.
// It is AddMessageContext without a cancellation scope; callers inside a turn
// pass the turn's context instead, so a compaction cannot outlive the turn.
func (m *MessageContextManager) AddMessage(message types.Message) error {
	return m.AddMessageContext(context.Background(), message)
}

// AddMessageContext is AddMessage with a context that bounds the compaction.
//
// The compaction can call the model (LLM compactor). Running it on a
// context.Background() meant a stalled provider held the manager — and the
// turn waiting on it — past the turn's own deadline, with nothing able to stop
// it. Passing the turn context bounds it.
func (m *MessageContextManager) AddMessageContext(ctx context.Context, message types.Message) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()

	message = cloneMessage(message)
	m.canonical = append(m.canonical, cloneMessage(message))
	if message.Role == "tool" {
		message.Content = truncateContextText(message.Content, m.toolOutputLimitChars())
	}

	m.active = append(m.active, message)
	m.applyMicrocompactLocked()

	var (
		summary    string
		dropped    int
		compactErr error
		hardErr    error
	)
	if m.needsCompactionLocked() {
		summary, dropped, compactErr = m.compactLocked(ctx)
	}
	if compactErr == nil && m.exceedsHardLimitLocked() {
		hardErr = fmt.Errorf("%w: estimated %d tokens", ErrContextLimit, m.estimateMessagesLocked())
	}
	hook := m.compactHook
	m.mu.Unlock()

	// Outside the lock: the hook writes a session event, and holding the
	// manager mutex across that append serialised every reader behind disk IO.
	if compactErr == nil && summary != "" && hook != nil {
		hook(summary, dropped)
	}
	if compactErr != nil {
		return compactErr
	}
	return hardErr
}

func (m *MessageContextManager) Messages() []types.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneMessages(m.active)
}

func (m *MessageContextManager) CanonicalMessages() []types.Message {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneMessages(m.canonical)
}

func (m *MessageContextManager) RestoreActive(messages []types.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.active = nil
	for _, message := range messages {
		message = cloneMessage(message)
		if message.Role == "tool" {
			message.Content = truncateContextText(message.Content, m.toolOutputLimitChars())
		}
		m.active = append(m.active, message)
	}
	if m.exceedsHardLimitLocked() {
		return fmt.Errorf("%w: estimated %d tokens", ErrContextLimit, m.estimateMessagesLocked())
	}
	return nil
}

func (m *MessageContextManager) SetToolSchemas(schemas []types.JSONSchema) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tools = cloneSchemas(schemas)
}

func (m *MessageContextManager) ToolSchemaHash() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return hashJSON(m.tools)
}

func (m *MessageContextManager) EstimateTokens() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.estimateMessagesLocked()
}

func (m *MessageContextManager) NeedsCompaction() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.needsCompactionLocked()
}

func (m *MessageContextManager) Compact(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	summary, dropped, err := m.compactLocked(ctx)
	hook := m.compactHook
	m.mu.Unlock()
	// Outside the lock: the hook writes a session event, and doing that while
	// holding the manager mutex serialised every other reader (Messages, Stats)
	// behind a disk append.
	if err == nil && summary != "" && hook != nil {
		hook(summary, dropped)
	}
	return err
}

// CompactForce performs one deterministic compaction even when the local
// estimator did not cross the configured threshold. It is used by the Agent
// loop after a provider reports that the request is too large.
func (m *MessageContextManager) CompactForce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	summary, dropped, err := m.compactLocked(ctx, true)
	hook := m.compactHook
	m.mu.Unlock()
	if err == nil && summary != "" && hook != nil {
		hook(summary, dropped)
	}
	return err
}

func (m *MessageContextManager) Reset() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.canonical = nil
	m.active = nil
	return nil
}

// CompactionCount returns the number of active-context compactions performed
// by this manager. It is used for request observability only.
func (m *MessageContextManager) CompactionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.compactions
}

// SetCompactHook installs an optional callback invoked after each successful
// active-context compaction. It receives the merged summary and the number of
// dropped message groups. The hook is invoked while the manager lock is held,
// so it must not call back into the manager.
func (m *MessageContextManager) SetCompactHook(hook func(summary string, droppedCount int)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.compactHook = hook
}

// ContextStats describes the bounded context currently sent to the model.
// It is intentionally a summary; callers should not persist full prompt
// contents merely to observe context behavior.
type ContextStats struct {
	MessageCount      int
	EstimatedTokens   int
	CompactionCount   int
	MicrocompactCount int
	CanonicalCount    int
	ToolSchemaCount   int
}

func (m *MessageContextManager) Stats() ContextStats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return ContextStats{
		MessageCount:      len(m.active),
		EstimatedTokens:   m.estimateMessagesLocked(),
		CompactionCount:   m.compactions,
		MicrocompactCount: m.microcompacts,
		CanonicalCount:    len(m.canonical),
		ToolSchemaCount:   len(m.tools),
	}
}

func (m *MessageContextManager) estimateMessagesLocked() int {
	// Media bytes are resolved only in the provider adapter. They are not
	// prompt text and must never be charged as base64 characters here.
	return m.estimator.EstimateMessages(m.active) + m.estimator.EstimateTools(m.tools)
}

func (m *MessageContextManager) needsCompactionLocked() bool {
	limit := m.compactionLimitTokens()
	return limit > 0 && m.estimateMessagesLocked() > limit
}

func (m *MessageContextManager) exceedsHardLimitLocked() bool {
	limit := m.hardLimitTokens()
	return limit > 0 && m.controllableTokensLocked() > limit
}

// controllableTokensLocked estimates what compaction can actually act on. The
// harness-injected prefix is rebuilt from records, state and mailbox every turn
// and must not be folded away (that would erase who said what), so counting it
// toward the hard limit would fail a turn over content the engine assembled
// itself.
func (m *MessageContextManager) controllableTokensLocked() int {
	controllable := make([]types.Message, 0, len(m.active))
	for _, message := range m.active {
		if message.Injected {
			continue
		}
		controllable = append(controllable, message)
	}
	return m.estimator.EstimateMessages(controllable) + m.estimator.EstimateTools(m.tools)
}

func (m *MessageContextManager) targetLimitTokens() int {
	return m.effectiveRatioLimit(m.config.TargetRatio)
}

func (m *MessageContextManager) compactionLimitTokens() int {
	return m.effectiveRatioLimit(m.config.CompactionThreshold)
}

func (m *MessageContextManager) hardLimitTokens() int {
	return m.effectiveRatioLimit(m.config.HardLimitRatio)
}

func (m *MessageContextManager) effectiveRatioLimit(ratio float64) int {
	if m.config.MaxInputTokens <= 0 {
		return 0
	}
	// Reserve space for the next model output. The ratio limits describe the
	// model input capacity, while OutputReserveRatio is subtracted from the
	// usable input budget.
	usableRatio := 1 - m.config.OutputReserveRatio
	if ratio > usableRatio {
		ratio = usableRatio
	}
	return int(float64(m.config.MaxInputTokens) * ratio)
}

func (m *MessageContextManager) toolOutputLimitChars() int {
	if m.config.MaxToolOutputChars > 0 {
		return m.config.MaxToolOutputChars
	}
	if m.config.MaxInputTokens > 0 {
		limit := int(float64(m.config.MaxInputTokens) * m.config.ToolOutputRatio * 4)
		if limit > 0 {
			return limit
		}
	}
	return 64 * 1024
}

// compactLocked rewrites the active context and reports what it dropped. It
// returns the summary and the number of dropped groups instead of invoking the
// hook itself: the hook is caller-facing state (a session write) and must not
// run under m.mu. Callers hold the lock.
func (m *MessageContextManager) compactLocked(ctx context.Context, force ...bool) (string, int, error) {
	if err := contextErr(ctx); err != nil {
		return "", 0, err
	}
	forced := len(force) > 0 && force[0]
	if !forced && !m.needsCompactionLocked() {
		if m.exceedsHardLimitLocked() {
			return "", 0, fmt.Errorf("%w: estimated %d tokens", ErrContextLimit, m.estimateMessagesLocked())
		}
		return "", 0, nil
	}

	system, anchor, existingSummary, groups := splitContextLayers(m.active)
	groups = microcompactGroups(groups, len(groups)-m.config.RecentMessageGroups, m.config, &m.microcompacts)
	prefix := cloneMessages(system)
	prefix = append(prefix, anchor...)
	target := m.targetLimitTokens()
	if target <= 0 {
		target = m.compactionLimitTokens()
	}
	if target <= 0 {
		target = m.hardLimitTokens()
	}

	prefixTokens := m.estimator.EstimateMessages(prefix)
	remaining := target - prefixTokens - m.estimator.EstimateTools(m.tools)
	if remaining < 0 {
		remaining = 0
	}

	var selected []types.Message
	selectedTokens := 0
	dropped := make([][]types.Message, 0)
	// The loop walks newest-first because the budget does (keep the tail), but
	// `dropped` must come out oldest-first: every consumer reads it
	// chronologically — the mechanical summary, the LLM compactor's
	// "Errors / Current Work / Next Step" template, and the message the model
	// finally reads. Prepending reverses the order for free, and unlike a
	// post-hoc reverse it stays correct on the forced branch below, where
	// dropped is filled by appending a prefix of groups.
	for i := len(groups) - 1; i >= 0; i-- {
		if err := contextErr(ctx); err != nil {
			return "", 0, err
		}
		group := groups[i]
		groupTokens := m.estimator.EstimateMessages(group)
		if len(selected) == 0 || selectedTokens+groupTokens <= remaining {
			selected = append(cloneMessages(group), selected...)
			selectedTokens += groupTokens
			continue
		}
		dropped = append([][]types.Message{group}, dropped...)
	}

	if forced && len(dropped) == 0 && len(groups) > m.config.RecentMessageGroups {
		keepFrom := len(groups) - m.config.RecentMessageGroups
		dropped = append(dropped, groups[:keepFrom]...)
		selected = flattenMessageGroups(groups[keepFrom:])
	}
	if len(dropped) == 0 && len(selected) == 0 {
		dropped = groups
	}

	candidate := cloneMessages(prefix)
	nextSummary, err := m.compactor.Compact(ctx, dropped)
	if err != nil || strings.TrimSpace(nextSummary) == "" {
		nextSummary = buildContextSummary(dropped, 0)
	}
	summary := mergeCompactionSummary(existingSummary, nextSummary)
	if summary != "" {
		// The summary budget follows the same rule as hardLimit below: with no
		// configured capacity `target` is 0, and fitCompactionSummary treats a
		// non-positive budget as "no room", silently dropping the summary
		// while the messages it describes have already been dropped. Fall back
		// to a fixed budget so a compression still hands the model an account
		// of what it lost.
		summaryBudget := target - m.estimator.EstimateTools(m.tools)
		if summaryBudget <= 0 {
			summaryBudget = defaultSummaryTokenBudget
		}
		if compacted := fitCompactionSummary(
			summary,
			prefix,
			m.estimator,
			summaryBudget,
		); compacted != nil {
			candidate = append(candidate, *compacted)
		}
	}
	candidate = append(candidate, selected...)

	hardLimit := m.hardLimitTokens() - m.estimator.EstimateTools(m.tools)
	if hardLimit < 0 {
		hardLimit = 0
	}
	// hardLimit == 0 means the manager has no input capacity to budget
	// against: contextConfig falls back to the provider's MaxInputTokens, and
	// a provider that does not implement ModelContextSizer leaves it at zero.
	// Trimming to a zero limit is not "make it fit" — trimToolMessagesToLimit
	// would empty every tool result and then the summary would be removed
	// too, producing a conversation with the history deleted. This is the path
	// CompactForce takes after a provider reports the prompt is too large, so
	// the degraded outcome is a silently emptied context followed by a retry
	// of the same request. Drop what the window rule selected and keep the
	// rest verbatim instead.
	if hardLimit > 0 {
		if m.estimator.EstimateMessages(candidate) > hardLimit {
			candidate, _ = trimToolMessagesToLimit(candidate, m.estimator, hardLimit)
		}
		if m.estimator.EstimateMessages(candidate) > hardLimit {
			candidate, _ = trimCompactionSummaryToLimit(candidate, m.estimator, hardLimit)
		}
		if m.estimator.EstimateMessages(candidate) > hardLimit && hasCompactionSummary(candidate) {
			// A summary is useful but never more important than preserving the
			// current active messages. Remove its text once more before failing.
			candidate = removeCompactionSummary(candidate)
			candidate, _ = trimToolMessagesToLimit(candidate, m.estimator, hardLimit)
		}
	}

	m.active = candidate
	m.compactions++
	if m.exceedsHardLimitLocked() {
		return summary, len(dropped), fmt.Errorf("%w after compaction: estimated %d tokens", ErrContextLimit, m.estimateMessagesLocked())
	}
	return summary, len(dropped), nil
}

func (m *MessageContextManager) applyMicrocompactLocked() {
	system, anchor, existingSummary, groups := splitContextLayers(m.active)
	before := len(groups) - m.config.RecentMessageGroups
	if before <= 0 {
		return
	}
	compactedGroups := microcompactGroups(groups, before, m.config, &m.microcompacts)
	if m.microcompacts == 0 {
		return
	}

	candidate := cloneMessages(system)
	candidate = append(candidate, anchor...)
	if existingSummary != "" {
		candidate = append(candidate, types.Message{
			Role:    "user",
			Content: appendCompactionSummary("", existingSummary),
		})
	}
	candidate = append(candidate, flattenMessageGroups(compactedGroups)...)
	m.active = candidate
}

func microcompactGroups(groups [][]types.Message, before int, config types.ContextConfig, count *int) [][]types.Message {
	if before <= 0 {
		return groups
	}
	result := make([][]types.Message, len(groups))
	for i, group := range groups {
		result[i] = cloneMessages(group)
		if i >= before {
			continue
		}
		for j, message := range result[i] {
			if message.Role != "tool" {
				continue
			}
			original := message.Content
			compacted := microcompactToolContent(message.ToolName, original, config)
			if compacted != original {
				result[i][j].Content = compacted
				if count != nil {
					(*count)++
				}
			}
		}
	}
	return result
}

func flattenMessageGroups(groups [][]types.Message) []types.Message {
	var result []types.Message
	for _, group := range groups {
		result = append(result, cloneMessages(group)...)
	}
	return result
}

func microcompactToolContent(toolName, content string, config types.ContextConfig) string {
	if utf8.RuneCountInString(content) <= config.MicrocompactThresholdChars {
		return content
	}
	limit := config.MicrocompactMaxChars
	if limit <= 0 {
		limit = 4096
	}
	lines := strings.Split(content, "\n")
	if len(lines) <= 4 {
		return truncateContextText(content, limit)
	}
	lineBudget := limit / 120
	if lineBudget < 2 {
		lineBudget = 2
	}
	if lineBudget*2 >= len(lines) {
		return truncateContextText(content, limit)
	}
	head := strings.Join(lines[:lineBudget], "\n")
	tail := strings.Join(lines[len(lines)-lineBudget:], "\n")
	if toolName == "" {
		toolName = "Tool"
	}
	return truncateContextText(
		fmt.Sprintf("[%s result microcompacted]\n%s\n...\n%s", toolName, head, tail),
		limit,
	)
}

func hasCompactionSummary(messages []types.Message) bool {
	for _, message := range messages {
		if message.Role == "user" && strings.Contains(message.Content, compactedContextMarker) {
			return true
		}
	}
	return false
}

func removeCompactionSummary(messages []types.Message) []types.Message {
	result := make([]types.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == "user" && strings.Contains(message.Content, compactedContextMarker) {
			continue
		}
		result = append(result, cloneMessage(message))
	}
	return result
}

const compactedContextMarker = "## Compacted Agent Context\n"

// compactedContextPreamble is prepended to a compaction summary so the model
// understands the session continued across a context reset.
const compactedContextPreamble = "This session is being continued from a previous conversation that ran out of context. The summary below covers the earlier portion of the conversation."

func trimToolMessagesToLimit(messages []types.Message, estimator ContextTokenEstimator, maxTokens int) ([]types.Message, bool) {
	result := cloneMessages(messages)
	for estimator.EstimateMessages(result) > maxTokens {
		index := -1
		longest := 0
		for i, message := range result {
			if message.Role != "tool" {
				continue
			}
			length := utf8.RuneCountInString(message.Content)
			if length > longest {
				longest = length
				index = i
			}
		}
		if index < 0 || longest == 0 {
			return result, false
		}

		low, high := 0, longest-1
		best := -1
		for low <= high {
			mid := low + (high-low)/2
			candidate := cloneMessages(result)
			candidate[index].Content = truncateContextText(result[index].Content, mid)
			if estimator.EstimateMessages(candidate) <= maxTokens {
				best = mid
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
		if best < 0 {
			result[index].Content = ""
		} else {
			result[index].Content = truncateContextText(result[index].Content, best)
		}
	}
	return result, true
}

func trimCompactionSummaryToLimit(messages []types.Message, estimator ContextTokenEstimator, maxTokens int) ([]types.Message, bool) {
	result := cloneMessages(messages)
	for estimator.EstimateMessages(result) > maxTokens {
		index := -1
		var base, summary string
		for i, message := range result {
			if message.Role != "user" || !strings.Contains(message.Content, compactedContextMarker) {
				continue
			}
			index = i
			base, summary = stripCompactionSummary(message.Content)
			break
		}
		if index < 0 || summary == "" {
			return result, false
		}

		runes := []rune(summary)
		low, high := 0, len(runes)
		best := -1
		for low <= high {
			mid := low + (high-low)/2
			candidate := cloneMessages(result)
			candidate[index].Content = appendCompactionSummary(base, string(runes[:mid]))
			if estimator.EstimateMessages(candidate) <= maxTokens {
				best = mid
				low = mid + 1
			} else {
				high = mid - 1
			}
		}
		if best < 0 {
			result[index].Content = base
		} else {
			result[index].Content = appendCompactionSummary(base, string(runes[:best]))
		}
	}
	return result, true
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

func splitContextLayers(messages []types.Message) ([]types.Message, []types.Message, string, [][]types.Message) {
	var system []types.Message
	index := 0
	for index < len(messages) && messages[index].Role == "system" {
		system = append(system, cloneMessage(messages[index]))
		index++
	}

	var anchor []types.Message
	var existingSummary string
	if index < len(messages) {
		first := cloneMessage(messages[index])
		base, summary := stripCompactionSummary(first.Content)
		if summary != "" {
			first.Content = base
			existingSummary = summary
		}
		anchor = append(anchor, first)
		index++
		// Everything the harness injected for this turn stays with the anchor.
		// Those messages carry who said what (role + name) and are rebuilt from
		// records, state and mailbox every turn, so folding them into a summary
		// would erase the provenance they exist to express.
		for index < len(messages) && messages[index].Injected {
			anchor = append(anchor, cloneMessage(messages[index]))
			index++
		}
	}

	var groups [][]types.Message
	for index < len(messages) {
		message := messages[index]
		if message.Role == "user" && strings.Contains(message.Content, compactedContextMarker) {
			_, summary := stripCompactionSummary(message.Content)
			existingSummary = mergeCompactionSummary(existingSummary, summary)
			index++
			continue
		}
		group := []types.Message{cloneMessage(message)}
		index++
		if message.Role == "assistant" && len(message.ToolCalls) > 0 {
			for index < len(messages) && messages[index].Role == "tool" {
				group = append(group, cloneMessage(messages[index]))
				index++
			}
		}
		groups = append(groups, group)
	}
	return system, anchor, existingSummary, groups
}

func buildContextSummary(groups [][]types.Message, maxChars int) string {
	var parts []string
	for _, group := range groups {
		for _, message := range group {
			if len(message.ToolCalls) > 0 {
				names := make([]string, 0, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					names = append(names, call.Name)
				}
				parts = append(parts, fmt.Sprintf("assistant requested tools: %s", strings.Join(names, ", ")))
				continue
			}
			content := strings.TrimSpace(message.Content)
			if content == "" {
				continue
			}
			// User messages carry the original intent and constraint changes.
			// Preserve them verbatim to prevent goal drift; only truncate
			// assistant/tool content which is more disposable.
			if message.Role == "user" {
				parts = append(parts, fmt.Sprintf("%s: %s", message.Role, content))
				continue
			}
			parts = append(parts, fmt.Sprintf("%s: %s", message.Role, truncateContextText(content, 500)))
		}
	}
	summary := strings.Join(parts, "\n")
	if maxChars > 0 {
		return truncateContextText(summary, maxChars)
	}
	return summary
}

func fitCompactionSummary(
	summary string,
	base []types.Message,
	estimator ContextTokenEstimator,
	maxTokens int,
) *types.Message {
	if strings.TrimSpace(summary) == "" || maxTokens <= 0 {
		return nil
	}

	runes := []rune(summary)
	low, high := 1, len(runes)
	var best *types.Message
	for low <= high {
		mid := low + (high-low)/2
		message := types.Message{
			Role:    "user",
			Content: appendCompactionSummary("", string(runes[:mid])),
		}
		candidate := append(cloneMessages(base), message)
		if estimator.EstimateMessages(candidate) <= maxTokens {
			copyMessage := cloneMessage(message)
			best = &copyMessage
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return best
}

func splitCompactionSummary(content string) (string, string) {
	index := strings.Index(content, compactedContextMarker)
	if index < 0 {
		return content, ""
	}
	return strings.TrimSpace(content[:index]), strings.TrimSpace(content[index+len(compactedContextMarker):])
}

func stripCompactionSummary(content string) (string, string) {
	return splitCompactionSummary(content)
}

func appendCompactionSummary(base, summary string) string {
	base = strings.TrimSpace(base)
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return base
	}
	prefixed := compactedContextPreamble + "\n\n" + compactedContextMarker + summary
	if base == "" {
		return prefixed
	}
	return base + "\n\n" + prefixed
}

func mergeCompactionSummary(existing, next string) string {
	existing = strings.TrimSpace(existing)
	next = strings.TrimSpace(next)
	switch {
	case existing == "":
		return next
	case next == "":
		return existing
	default:
		return truncateContextText(existing+"\n"+next, 16000)
	}
}

type defaultContextTokenEstimator struct{}

func (defaultContextTokenEstimator) EstimateMessages(messages []types.Message) int {
	total := 0
	for _, message := range messages {
		total += estimateContextText(message.Role) + estimateContextText(message.Content) + 4
		// The participant name travels in the request (OpenAI `name`), so it is
		// billed input like any other field.
		total += estimateContextText(message.TeamID)
		if len(message.ToolCalls) > 0 {
			data, _ := json.Marshal(message.ToolCalls)
			total += estimateContextText(string(data))
		}
		total += estimateContextText(message.ToolCallID)
		total += estimateContextText(message.ToolName)
	}
	return total
}

func (defaultContextTokenEstimator) EstimateTools(tools []types.JSONSchema) int {
	total := 0
	for _, tool := range tools {
		data, _ := json.Marshal(tool)
		total += estimateContextText(string(data)) + 8
	}
	return total
}

// estimateContextText approximates the token count of one string.
//
// "Four characters per token" holds for English and is wrong by a factor of
// three to four for Chinese, Japanese and Korean: those are roughly one token
// per character to the tokenizers the providers use. Dividing their rune count
// by four under-reported the prompt size, so a CJK conversation hit the provider
// with "prompt is too long" long before the local budget noticed — and the local
// budget is what decides when to compact.
//
// ASCII keeps the 4:1 ratio; every other rune is charged one token. Over-
// estimating is the safe direction here: it compacts sooner, never later.
func estimateContextText(text string) int {
	if text == "" {
		return 0
	}
	ascii := 0
	other := 0
	for _, r := range text {
		if r < utf8.RuneSelf {
			ascii++
			continue
		}
		other++
	}
	return (ascii+3)/4 + other
}

func truncateContextText(text string, maxChars int) string {
	if maxChars <= 0 || utf8.RuneCountInString(text) <= maxChars {
		return text
	}
	runes := []rune(text)
	const suffix = "\n...[truncated]"
	suffixRunes := []rune(suffix)
	if maxChars <= len(suffixRunes) {
		return string(runes[:maxChars])
	}
	return string(runes[:maxChars-len(suffixRunes)]) + suffix
}

func cloneMessage(message types.Message) types.Message {
	message.ToolCalls = append([]types.ToolCall(nil), message.ToolCalls...)
	if len(message.Parts) > 0 {
		message.Parts = make([]types.ContentPart, len(message.Parts))
		for i, part := range message.Parts {
			message.Parts[i] = part
			if part.Media != nil {
				media := *part.Media
				message.Parts[i].Media = &media
			}
		}
	}
	return message
}

func cloneMessages(messages []types.Message) []types.Message {
	if messages == nil {
		return nil
	}
	result := make([]types.Message, len(messages))
	for i, message := range messages {
		result[i] = cloneMessage(message)
	}
	return result
}

func cloneSchemas(schemas []types.JSONSchema) []types.JSONSchema {
	if schemas == nil {
		return nil
	}
	result := make([]types.JSONSchema, len(schemas))
	copy(result, schemas)
	return result
}
