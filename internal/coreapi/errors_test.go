package coreapi

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/yuha0/vigo/internal/errs"
	"github.com/yuha0/vigo/internal/library"
	"github.com/yuha0/vigo/internal/samsung"
	"github.com/yuha0/vigo/internal/source"
	"github.com/yuha0/vigo/internal/tv"
)

func TestUserMessages(t *testing.T) {
	tests := []struct {
		err  error
		code connect.Code
		want string
	}{
		{errs.New(library.ErrInvalid, "path %q is not allowed", "../x"), connect.CodeInvalidArgument, `Path "../x" is not allowed.`},
		{errs.New(source.ErrExists, "a file named %s already exists", "a.jpg"), connect.CodeAlreadyExists, "A file named a.jpg already exists."},
		{tv.ErrNotPaired, connect.CodeFailedPrecondition, "Not paired with the TV."},
		{fmt.Errorf("select image: %w", errs.New(tv.ErrNotFound, "no artwork %s", "X")), connect.CodeNotFound, "Select image: no artwork X."},
		{&samsung.TVError{Request: "change_matte", Code: "-7"}, connect.CodeFailedPrecondition, "Change_matte refused with error_code -7."},
		{errors.New("db: dial tcp 10.0.0.1:5432: connection refused"), connect.CodeUnknown, "Something went wrong in vigo-core; the details are in its log."},
	}
	for _, tt := range tests {
		code := codeOf(tt.err)
		if code != tt.code {
			t.Errorf("%v: code %v, want %v", tt.err, code, tt.code)
		}
		if got := userMessage(tt.err, code); got != tt.want {
			t.Errorf("%v: message %q, want %q", tt.err, got, tt.want)
		}
	}
	if got := sentence("vigo-home is unavailable"); got != "vigo-home is unavailable." {
		t.Errorf("sentence = %q", got)
	}
	ce := toConnect(errs.New(library.ErrNotFound, "no artwork %s", "sha256:x"))
	var c *connect.Error
	if !errors.As(ce, &c) || c.Message() != "No artwork sha256:x." || !errors.Is(ce, library.ErrNotFound) {
		t.Errorf("toConnect = %v", ce)
	}
}
