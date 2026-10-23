package fielderr

import (
	"errors"
	"fmt"
	"testing"
)

func TestNew(t *testing.T) {
	err := New("limits.memory", "must be between %d and %d", 0, 10)

	e, ok := Of(err)
	if !ok {
		t.Fatal("Of did not find the error")
	}
	if e.Field != "limits.memory" || e.Message != "must be between 0 and 10" {
		t.Errorf("got %+v", e)
	}
	if err.Error() != "limits.memory: must be between 0 and 10" {
		t.Errorf("Error() = %q", err.Error())
	}
}

func TestOfFindsAWrappedError(t *testing.T) {
	err := fmt.Errorf("create server: %w", New("name", "is required"))

	e, ok := Of(err)
	if !ok || e.Field != "name" {
		t.Errorf("Of = %+v, %v", e, ok)
	}
}

func TestOfRefusesOtherErrors(t *testing.T) {
	if e, ok := Of(errors.New("plain")); ok || e != nil {
		t.Errorf("Of = %+v, %v, want nothing", e, ok)
	}
	if _, ok := Of(nil); ok {
		t.Error("Of(nil) must find nothing")
	}
}
