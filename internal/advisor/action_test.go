package advisor

import (
	"encoding/json"
	"testing"
)

func TestActionParsing(t *testing.T) {
	for _, ok := range []string{"adopt_source", "adopt_target", "keep_separate"} {
		var r ConflictExplanationResponse
		if err := json.Unmarshal([]byte(`{"recommended_action":"`+ok+`"}`), &r); err != nil || string(r.RecommendedAction) != ok {
			t.Errorf("%s: got %q, err %v", ok, r.RecommendedAction, err)
		}
	}
	for _, bad := range []string{`"adopt_everything"`, `""`, `"ADOPT_SOURCE"`, `7`} {
		var r ConflictExplanationResponse
		if err := json.Unmarshal([]byte(`{"recommended_action":`+bad+`}`), &r); err == nil {
			t.Errorf("%s: expected an error, got action %q", bad, r.RecommendedAction)
		}
	}
}
