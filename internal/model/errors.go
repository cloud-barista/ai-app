package model

import (
	"errors"
	"fmt"
)

// ErrValidation marks a caller-facing input problem, so handlers can map it to
// 400 without inspecting the message text.
var ErrValidation = errors.New("validation failed")

// ValidationError carries a message written for the API caller.
//
// Message is deliberately separate from Error(): Error() is what gets wrapped
// and logged, Message is what a caller sees, and the two have different audiences.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string { return "validation failed: " + e.Message }

// Is lets errors.Is(err, ErrValidation) match any ValidationError.
func (e *ValidationError) Is(target error) bool { return target == ErrValidation }

// ValidationMessage returns the caller-facing text of a validation error, or an
// empty string when err does not carry one.
func ValidationMessage(err error) string {
	var v *ValidationError
	if errors.As(err, &v) {
		return v.Message
	}
	return ""
}

func errRequired(field string) error {
	return &ValidationError{Message: fmt.Sprintf("%s required", field)}
}

func errInvalid(field, reason string) error {
	return &ValidationError{Message: fmt.Sprintf("%s %s", field, reason)}
}
