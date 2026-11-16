package graft

import (
	"context"
	"errors"
	"fmt"
)

// context returns the context the evaluation runs under, or
// context.Background when none was given.
func (ev *Evaluator) context() context.Context {
	if ev.ctx == nil {
		return context.Background()
	}
	return ev.ctx
}

// canceled returns nil while ctx is live, and otherwise an error that
// names the step and wraps ctx.Err(), so errors.Is matches
// context.Canceled or context.DeadlineExceeded. The step names where
// evaluation stopped, such as an operator's path, and never a value.
func canceled(ctx context.Context, step string) error {
	return stopped(ctx, "evaluation", step)
}

// isCancel reports whether err is the cancellation of ctx, so a phase
// that stopped on it can hand that error back on its own.
func isCancel(ctx context.Context, err error) bool {
	cause := ctx.Err()
	return err != nil && cause != nil && errors.Is(err, cause)
}

// opStep names op for a cancellation message by its document path, such
// as $.meta.x, which is structure the caller wrote and not a value.
func opStep(op *Opcall) string {
	if op == nil || op.where == nil {
		return "the next operator"
	}
	if len(op.where.Nodes) == 0 {
		return "$"
	}
	return "$." + op.where.String()
}

// phaseStep names an evaluation phase for a cancellation message, such
// as "the eval phase".
func phaseStep(phase OperatorPhase) string {
	return "the " + phaseToString(phase) + " phase"
}

// opCanceled is canceled for the step of running op. It runs before
// every operator, so it names the step only once ctx is done, and a live
// context costs no allocation.
func opCanceled(ctx context.Context, op *Opcall) error {
	if ctx.Err() == nil {
		return nil
	}
	return canceled(ctx, opStep(op))
}

// stopped is canceled for a phase that is not evaluation. The verb names
// the phase, such as "parse", so the message reads "parse stopped before
// <step>" and wraps ctx.Err().
func stopped(ctx context.Context, verb, step string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s stopped before %s: %w", verb, step, err)
	}
	return nil
}
