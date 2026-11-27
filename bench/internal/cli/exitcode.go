package cli

import (
	"errors"

	"github.com/servtuner/servtuner/bench/internal/bundle"
	"github.com/servtuner/servtuner/bench/internal/engine"
	"github.com/servtuner/servtuner/bench/internal/validate"
)

// Exit codes (bench/CLAUDE.md), defined once.
const (
	ExitOK                = 0
	ExitRunFailed         = 1
	ExitInvalidSpec       = 2 // also bad flags and flag combinations
	ExitTargetUnreachable = 3
	ExitBundleWriteFailed = 4
)

// exitError carries the exit code a command chose.
type exitError struct {
	code int
	err  error
}

func (e exitError) Error() string { return e.err.Error() }
func (e exitError) Unwrap() error { return e.err }

// usageError reports bad flags or a bad combination of them (exit code 2).
func usageError(err error) error { return exitError{code: ExitInvalidSpec, err: err} }

// failure classifies the error of a command that ran. A failed bundle write
// wins over the other causes, because the bundle is then incomplete whatever
// else went wrong. An error nothing recognises is a failed run.
func failure(err error) error {
	code := ExitRunFailed
	switch {
	case errors.Is(err, bundle.ErrWrite):
		code = ExitBundleWriteFailed
	case errors.Is(err, validate.ErrInvalidSpec):
		code = ExitInvalidSpec
	case errors.Is(err, engine.ErrTargetUnreachable):
		code = ExitTargetUnreachable
	}
	return exitError{code: code, err: err}
}
