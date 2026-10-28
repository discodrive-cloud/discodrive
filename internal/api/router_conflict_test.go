package api

import (
	"testing"
)

func TestNewRouterNoPatternConflict(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NewRouter panicked (route conflict): %v", r)
		}
	}()
	_ = NewRouter(nil, nil, nil, nil, "", nil, nil, nil, nil, nil, nil, nil, false, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
}
