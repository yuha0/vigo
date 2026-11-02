package coreapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"connectrpc.com/connect"

	"github.com/yuha0/vigo/internal/imageprep"
	"github.com/yuha0/vigo/internal/library"
	"github.com/yuha0/vigo/internal/samsung"
	"github.com/yuha0/vigo/internal/source"
	"github.com/yuha0/vigo/internal/tv"
)

// codeOf maps service errors to Connect codes.
func codeOf(err error) connect.Code {
	var (
		ce     *connect.Error
		tvErr  *samsung.TVError
		protoE *samsung.ProtocolError
		netErr net.Error
	)
	switch {
	case errors.As(err, &ce):
		return ce.Code()
	case errors.Is(err, context.Canceled):
		return connect.CodeCanceled
	case errors.Is(err, library.ErrInvalid), errors.Is(err, source.ErrInvalidPath), errors.Is(err, imageprep.ErrUnsupported):
		return connect.CodeInvalidArgument
	case errors.Is(err, library.ErrNotFound), errors.Is(err, source.ErrNotFound):
		return connect.CodeNotFound
	case errors.Is(err, source.ErrExists):
		return connect.CodeAlreadyExists
	case errors.Is(err, library.ErrUnsupported):
		return connect.CodeFailedPrecondition
	case errors.Is(err, library.ErrNotRunning):
		return connect.CodeUnavailable
	case errors.Is(err, tv.ErrInvalidArgument):
		return connect.CodeInvalidArgument
	case errors.Is(err, tv.ErrNotFound):
		return connect.CodeNotFound
	case errors.Is(err, tv.ErrStandbyReplica), errors.Is(err, tv.ErrNotConnected):
		return connect.CodeUnavailable
	case errors.Is(err, tv.ErrNotPaired), errors.Is(err, tv.ErrNoArtHost),
		errors.Is(err, tv.ErrUnreachable), errors.Is(err, tv.ErrNoHost), errors.Is(err, tv.ErrPoweredOff),
		errors.Is(err, samsung.ErrUnauthorized), errors.Is(err, samsung.ErrNoArtHost),
		errors.As(err, &tvErr):
		return connect.CodeFailedPrecondition
	case errors.Is(err, tv.ErrWakeTimeout), errors.Is(err, samsung.ErrNoReply),
		errors.Is(err, context.DeadlineExceeded):
		return connect.CodeDeadlineExceeded
	case errors.As(err, &protoE):
		return connect.CodeInternal
	case errors.As(err, &netErr):
		if netErr.Timeout() {
			return connect.CodeDeadlineExceeded
		}
		return connect.CodeUnavailable
	}
	return connect.CodeUnknown
}

// userMessage is the text shown to people: the error's own message as a
// sentence. Unknown and internal errors carry implementation detail, so they
// get a generic sentence and the detail goes to the log.
func userMessage(err error, code connect.Code) string {
	if code == connect.CodeUnknown || code == connect.CodeInternal {
		slog.Default().Error("request failed", "code", code.String(), "err", err)
		return "Something went wrong in vigo-core; the details are in its log."
	}
	// TV refusals come from package samsung, whose errors carry its name.
	msg := strings.TrimPrefix(strings.TrimSpace(err.Error()), "samsung: ")
	if msg == "" {
		return code.String()
	}
	return sentence(msg)
}

// sentence capitalizes msg and ends it with a full stop.
func sentence(msg string) string {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return msg
	}
	r, size := utf8.DecodeRuneInString(msg)
	if unicode.IsLower(r) && !strings.HasPrefix(msg, "vigo") {
		msg = string(unicode.ToUpper(r)) + msg[size:]
	}
	if !strings.ContainsRune(".!?", rune(msg[len(msg)-1])) {
		msg += "."
	}
	return msg
}

// shownError carries the user message while keeping the cause for errors.Is
// and logs.
type shownError struct {
	msg   string
	cause error
}

func (e *shownError) Error() string { return e.msg }
func (e *shownError) Unwrap() error { return e.cause }

func toConnect(err error) error {
	if err == nil {
		return nil
	}
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce
	}
	code := codeOf(err)
	return connect.NewError(code, &shownError{msg: userMessage(err, code), cause: err})
}

// httpStatus follows the Connect protocol's code to HTTP status mapping.
func httpStatus(c connect.Code) int {
	switch c {
	case connect.CodeCanceled:
		return 499
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeOutOfRange:
		return http.StatusBadRequest
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeAlreadyExists, connect.CodeAborted:
		return http.StatusConflict
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	}
	return http.StatusInternalServerError
}

// writeError writes {"code": "...", "message": "..."} with the matching
// HTTP status, the shape Connect unary errors have.
func writeError(w http.ResponseWriter, code connect.Code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(httpStatus(code))
	json.NewEncoder(w).Encode(struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code.String(), sentence(msg)})
}

func writeErr(w http.ResponseWriter, err error) {
	var ce *connect.Error
	if errors.As(err, &ce) {
		writeError(w, ce.Code(), ce.Message())
		return
	}
	code := codeOf(err)
	writeError(w, code, userMessage(err, code))
}
