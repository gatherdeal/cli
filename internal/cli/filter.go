package cli

import (
	jsonv2 "encoding/json/v2"
	"slices"
	"strings"
)

// filterGroup decodes the fields needed for the operator check. The generated
// binding decodes the complete request.
type filterGroup struct {
	Edge   string         `json:"edge"`
	Groups []*filterGroup `json:"groups"`
	Rules  []struct {
		Field string `json:"field"`
		Op    string `json:"op"`
	} `json:"rules"`
}

func validateFilter(raw string, fields map[string][]string) error {
	var group *filterGroup
	if err := jsonv2.Unmarshal([]byte(raw), &group); err != nil {
		return usageError("invalid --filter JSON: %v", err)
	}
	return group.validateOperators(fields)
}

func (g *filterGroup) validateOperators(fields map[string][]string) error {
	// An edge changes which entity owns every field in its subtree.
	if g == nil || g.Edge != "" {
		return nil
	}
	for _, rule := range g.Rules {
		if allowed, ok := fields[rule.Field]; ok && !slices.Contains(allowed, rule.Op) {
			return usageError("--filter operator %q is invalid for field %q. Allowed operators are %s.", rule.Op, rule.Field, strings.Join(allowed, ", ")).
				WithDetails(map[string]any{"field": rule.Field, "operator": rule.Op, "allowed_operators": allowed})
		}
	}
	for _, sub := range g.Groups {
		if err := sub.validateOperators(fields); err != nil {
			return err
		}
	}
	return nil
}
