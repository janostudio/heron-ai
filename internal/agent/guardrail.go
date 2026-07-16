package agent

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/heron-ai/heron-engine/pkg/types"
)

type GuardrailChecker struct {
	inputRules  []types.GuardrailRule
	outputRules []types.GuardrailRule
}

func NewGuardrailChecker(inputRules, outputRules []types.GuardrailRule) *GuardrailChecker {
	return &GuardrailChecker{
		inputRules:  inputRules,
		outputRules: outputRules,
	}
}

func (g *GuardrailChecker) CheckInput(input string) error {
	for _, rule := range g.inputRules {
		if err := checkRule(input, rule); err != nil {
			return fmt.Errorf("input guardrail: %w", err)
		}
	}
	return nil
}

func (g *GuardrailChecker) CheckOutput(output string) error {
	for _, rule := range g.outputRules {
		if err := checkRule(output, rule); err != nil {
			return fmt.Errorf("output guardrail: %w", err)
		}
	}
	return nil
}

func checkRule(text string, rule types.GuardrailRule) error {
	switch rule.Type {
	case "regex":
		if rule.Pattern == "" {
			return nil
		}
		re, err := regexp.Compile(rule.Pattern)
		if err != nil {
			return fmt.Errorf("invalid regex pattern: %w", err)
		}
		if re.MatchString(text) {
			return fmt.Errorf("%s", rule.Message)
		}
	case "contains":
		if strings.Contains(text, rule.Pattern) {
			return fmt.Errorf("%s", rule.Message)
		}
	}
	return nil
}
