package router

import "testing"

func TestSetupRouterNoPanic(t *testing.T) {
	r := SetupRouter()
	if r == nil {
		t.Fatal("nil router")
	}
}
