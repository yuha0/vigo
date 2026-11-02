// Package errs builds errors that belong to a kind, a sentinel matched with
// errors.Is, while their text is only the specific message. API layers map
// the kind to a status and show the text as is, so it must read as a
// sentence fragment on its own, without a "kind: " prefix.
package errs

import "fmt"

type kindError struct {
	kinds []error
	msg   string
}

func (e *kindError) Error() string   { return e.msg }
func (e *kindError) Unwrap() []error { return e.kinds }

// New returns an error of kind with a formatted message. Arguments may
// include errors wrapped with %w.
func New(kind error, format string, a ...any) error {
	cause := fmt.Errorf(format, a...)
	kinds := []error{kind}
	if u, ok := cause.(interface{ Unwrap() error }); ok && u.Unwrap() != nil {
		kinds = append(kinds, u.Unwrap())
	} else if u, ok := cause.(interface{ Unwrap() []error }); ok {
		kinds = append(kinds, u.Unwrap()...)
	}
	return &kindError{kinds: kinds, msg: cause.Error()}
}

// Wrap returns cause as an error of kind too, keeping its message.
func Wrap(kind, cause error) error {
	return &kindError{kinds: []error{kind, cause}, msg: cause.Error()}
}
