package spec

import "testing"

func TestRepeatedGlobstarsStopAtVisitBudgetWithoutMatches(t *testing.T) {
	var nested func(int) any
	nested = func(depth int) any {
		if depth == 0 {
			return map[string]any{}
		}
		child := nested(depth - 1)
		return map[string]any{"a": []any{child, child}}
	}

	const budget = 128
	got, visited := valuesWithBudget(nested(12), "**.a.**.a.**.a.missing", budget)
	if len(got) != 0 {
		t.Fatalf("values = %+v, want no matches", got)
	}
	if visited != budget {
		t.Fatalf("visited %d path states, want the budget of %d", visited, budget)
	}
	if got, visited := valuesWithBudget(nested(1), "", 0); len(got) != 0 || visited != 0 {
		t.Fatalf("zero visit budget returned %v after %d visits", got, visited)
	}
}
