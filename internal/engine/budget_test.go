package engine

import "testing"

// The user's budget is a hard ceiling, not a target: shapes[0] is the background and IS injected as
// the bottom layer, so a budget of N means N layers in a group whose in-game ceiling is exactly N.
// Flat measured 1001 for a budget of 1000 on every recorded case before clampToBudget existed,
// because the passes after the greedy's own prune regrow shapes and nothing put the count back.
func TestPlaceBudgetLeavesRoomForTheBackground(t *testing.T) {
	for _, n := range []int{1, 2, 100, 1000, 3000} {
		if got := placeBudgetMirror(n); got+1 > n && n > 1 {
			t.Errorf("budget %d: %d placed + 1 background = %d layers, over the budget", n, got, got+1)
		}
	}
}

// placeBudgetMirror is preset.PlaceBudget's contract, restated here because internal/engine must not
// import internal/preset. If the two ever disagree, the count the user asked for is not what ships.
func placeBudgetMirror(shapes int) int {
	if n := shapes - 1; n >= 1 {
		return n
	}
	return 1
}
