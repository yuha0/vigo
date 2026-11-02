package errs

import (
	"errors"
	"testing"
)

func TestKinds(t *testing.T) {
	kind := errors.New("invalid argument")
	cause := errors.New("root cause")
	e := New(kind, "path %q is outside the root: %w", "../x", cause)
	if e.Error() != `path "../x" is outside the root: root cause` {
		t.Errorf("message = %q", e)
	}
	if !errors.Is(e, kind) || !errors.Is(e, cause) {
		t.Error("lost a kind")
	}
	w := Wrap(kind, cause)
	if w.Error() != "root cause" || !errors.Is(w, kind) || !errors.Is(w, cause) {
		t.Errorf("wrap = %q", w)
	}
}
