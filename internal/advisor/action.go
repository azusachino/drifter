package advisor

import (
	"encoding/json"
	"fmt"
)

// Action is the resolution an advisor recommends for a status conflict.
// Parsing rejects any value outside the three below, so a caller that holds
// an Action never has to guard against an unknown one.
type Action string

const (
	// ActionAdoptSource recommends writing the source status to the target.
	ActionAdoptSource Action = "adopt_source"
	// ActionAdoptTarget recommends keeping the target status as the baseline.
	ActionAdoptTarget Action = "adopt_target"
	// ActionKeepSeparate recommends leaving both sides unchanged.
	ActionKeepSeparate Action = "keep_separate"
)

// UnmarshalJSON accepts only the defined actions.
func (a *Action) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("advisor action must be a string: %w", err)
	}
	switch v := Action(s); v {
	case ActionAdoptSource, ActionAdoptTarget, ActionKeepSeparate:
		*a = v
		return nil
	}
	return fmt.Errorf("unknown advisor action %q", s)
}
