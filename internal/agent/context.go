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
	summarizer    Summarizer
	canonical     []types.Message
	active        []types.Message
	tools         []types.JSONSchema
	compactions   int
	microcompacts int
}

func NewContextManager(config types.ContextConfig) *MessageContextManager {
	return NewContextManagerWithEstimator(config, defaultContextTokenEstimator{})
}

func NewContextManagerWithEstimator(config types.ContextConfig, estimator ContextTokenEstimator) *MessageContextManager {
	return NewContextManagerWithSummarizer(config, estimator, nil)
}

func NewContextManagerWithSummarizer(config types.ContextConfig, estimator ContextTokenEstimator, summarizer Summarizer) *MessageContextManager {
	if estimator == nil {
		estimator = defaultContextTokenEstimator{}
	}
	if summarizer == nil {
		summarizer = mechanicalSummarizer{}
	}
	return &MessageContextManager{
		config:     config.WithDefaults(),
		estimator:  estimator,
		summarizer: summarizer,
	}
}

func (m *MessageContextManager) AddMessage(message types.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	message = cloneMessage(message)
	m.canonical = append(m.canonical, cloneMessage(message))
	if message.Role == "tool" {
		message.Content = truncateContextText(message.Content, m.toolOutputLimitChars())
	}

	m.active = append(m.active, message)
	m.applyMicrocompactLocked()

	if m.needsCompactionLocked() {
		if err := m.compactLocked(context.Background()); err != nil {
			return err
		}
	}
	if m.exceedsHardLimitLocked() {
		return fmt.Errorf("%w: estimated %d tokens", ErrContextLimit, m.estimateMessagesLocked())
	}
	return nil
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
	defer m.mu.Unlock()
	return m.compactLocked(ctx)
}

// CompactForce performs one deterministic compaction even when the local
// estimator did not cross the configured threshold. It is used by the Agent
// loop after a provider reports that the request is too large.
func (m *MessageContextManager) CompactForce(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.compactLocked(ctx, true)
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
	return limit > 0 && m.estimateMessagesLocked() > limit
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

func (m *MessageContextManager) compactLocked(ctx context.Context, force ...bool) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	forced := len(force) > 0 && force[0]
	if !forced && !m.needsCompactionLocked() {
		if m.exceedsHardLimitLocked() {
			return fmt.Errorf("%w: estimated %d tokens", ErrContextLimit, m.estimateMessagesLocked())
		}
		return nil
	}

	system, anchor, existingSummary, groups := splitContextLayers(m.active)
	groups = microcompactGroups(groups, len(groups)-m.config.RecentMessageGroups, m.config, &m.microcompacts)
	prefix := cloneMessages(system)
	if anchor != nil {
		prefix = append(prefix, *anchor)
	}
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
	for i := len(groups) - 1; i >= 0; i-- {
		if err := contextErr(ctx); err != nil {
			return err
		}
		group := groups[i]
		groupTokens := m.estimator.EstimateMessages(group)
		if len(selected) == 0 || selectedTokens+groupTokens <= remaining {
			selected = append(cloneMessages(group), selected...)
			selectedTokens += groupTokens
			continue
		}
		dropped = append(dropped, group)
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
	nextSummary, err := m.summarizer.Summarize(ctx, dropped)
	if err != nil || strings.TrimSpace(nextSummary) == "" {
		nextSummary = buildContextSummary(dropped, 0)
	}
	summary := mergeCompactionSummary(existingSummary, nextSummary)
	if summary != "" {
		if compacted := fitCompactionSummary(
			summary,
			prefix,
			m.estimator,
			target-m.estimator.EstimateTools(m.tools),
		); compacted != nil {
			candidate = append(candidate, *compacted)
		}
	}
	candidate = append(candidate, selected...)

	hardLimit := m.hardLimitTokens() - m.estimator.EstimateTools(m.tools)
	if hardLimit < 0 {
		hardLimit = 0
	}
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

	m.active = candidate
	m.compactions++
	if m.exceedsHardLimitLocked() {
		return fmt.Errorf("%w after compaction: estimated %d tokens", ErrContextLimit, m.estimateMessagesLocked())
	}
	return nil
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
	if anchor != nil {
		candidate = append(candidate, *anchor)
	}
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

func splitContextLayers(messages []types.Message) ([]types.Message, *types.Message, string, [][]types.Message) {
	var system []types.Message
	index := 0
	for index < len(messages) && messages[index].Role == "system" {
		system = append(system, cloneMessage(messages[index]))
		index++
	}

	var anchor *types.Message
	var existingSummary string
	if index < len(messages) {
		first := cloneMessage(messages[index])
		base, summary := stripCompactionSummary(first.Content)
		if summary != "" {
			first.Content = base
			existingSummary = summary
		}
		anchor = &first
		index++
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
	if base == "" {
		return compactedContextMarker + summary
	}
	return base + "\n\n" + compactedContextMarker + summary
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

func estimateContextText(text string) int {
	if text == "" {
		return 0
	}
	return (utf8.RuneCountInString(text) + 3) / 4
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
