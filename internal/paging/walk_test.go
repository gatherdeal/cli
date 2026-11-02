package paging

import (
	"context"
	"fmt"
	"testing"

	"github.com/gatherdeal/cli/internal/output"
)

func TestGenericWalkContinuation(t *testing.T) {
	requests := 0
	result, err := Walk(context.Background(), "widgets", "initial", 10, 3, func(_ context.Context, after string, size int) (Page[int], error) {
		requests++
		if requests == 1 {
			if after != "initial" || size != 3 {
				t.Fatal(after, size)
			}
			return Page[int]{Items: []int{1, 2}, HasNextPage: true, NextCursor: "next"}, nil
		}
		if after != "next" || size != 1 {
			t.Fatal(after, size)
		}
		return Page[int]{Items: []int{3}, HasNextPage: true, NextCursor: "resume", CorrelationID: "last"}, nil
	})
	if err != nil || !result.Truncated || result.NextCursor != "resume" || result.CorrelationID != "last" || len(result.Items) != 3 {
		t.Fatalf("%+v %v", result, err)
	}
}
func TestGenericWalkBudget(t *testing.T) {
	calls := 0
	result, err := Walk(context.Background(), "widgets", "", 10, 1, func(context.Context, string, int) (Page[int], error) {
		calls++
		return Page[int]{HasNextPage: true, NextCursor: fmt.Sprint(calls)}, nil
	})
	if result != nil || err == nil || output.AsError(err).Code != output.CodeProtocol || calls != 9 {
		t.Fatalf("%+v %v %d", result, err, calls)
	}
}
func TestGenericWalkHardCap(t *testing.T) {
	calls := 0
	result, err := Walk(context.Background(), "widgets", "", 100, 0, func(_ context.Context, _ string, size int) (Page[int], error) {
		calls++
		return Page[int]{Items: make([]int, size), HasNextPage: true, NextCursor: fmt.Sprint(calls)}, nil
	})
	if err != nil || len(result.Items) != MaxItems || !result.Truncated || result.NextCursor != fmt.Sprint(calls) || calls != MaxItems/100 {
		t.Fatalf("%d items, truncated %v, cursor %q, %d calls, %v", len(result.Items), result.Truncated, result.NextCursor, calls, err)
	}
	// A cap above the hard cap is the hard cap.
	calls = 0
	result, err = Walk(context.Background(), "widgets", "", 100, MaxItems+1, func(_ context.Context, _ string, size int) (Page[int], error) {
		calls++
		return Page[int]{Items: make([]int, size), HasNextPage: true, NextCursor: fmt.Sprint(calls)}, nil
	})
	if err != nil || len(result.Items) != MaxItems || !result.Truncated {
		t.Fatalf("%+v %v", len(result.Items), err)
	}
}
