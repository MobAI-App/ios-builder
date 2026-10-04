// Package exitcode is the one table of process exit codes Builder uses, so
// scripts and agents can branch on why a command failed without parsing its
// message.
//
//	0    OK           the command did what it was asked
//	1    Failure      anything not listed below
//	2    Usage        bad flags or arguments, or an answer is needed that
//	                  only a flag can give without a terminal
//	3    Auth         a login or API key is missing or was rejected
//	4    BuildFailed  the CI run ended without success
//	5    Timeout      a wait ran past its time limit (--timeout)
//	130  Interrupted  Ctrl-C or SIGTERM
package exitcode

import (
	"context"
	"errors"
	"fmt"
)

// The exit codes. README "Using Builder from agents and CI" lists them too.
const (
	OK          = 0
	Failure     = 1
	Usage       = 2
	Auth        = 3
	BuildFailed = 4
	Timeout     = 5
	Interrupted = 130
)

// Error carries an exit code with an error; its message is the wrapped one.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// With tags err with code; a nil err stays nil.
func With(code int, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

// Usagef is a usage error (exit 2) with a formatted message.
func Usagef(format string, args ...any) error {
	return With(Usage, fmt.Errorf(format, args...))
}

// Code maps an error to its exit code: an explicit tag (With, or an error
// type with an ExitCode method) first, then an interrupted or expired
// context, then any error reporting Timeout() true, else Failure.
func Code(err error) int {
	if err == nil {
		return OK
	}
	var tagged *Error
	if errors.As(err, &tagged) {
		return tagged.Code
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return coded.ExitCode()
	}
	if errors.Is(err, context.Canceled) {
		return Interrupted
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Timeout
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return Timeout
	}
	return Failure
}
