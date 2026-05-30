package errors

import (
	stderrors "errors"
	"fmt"
)

var ErrVerificationRequired = stderrors.New("xiaohongshu verification required")

type VerificationError struct {
	Reason string
	URL    string
}

func (e *VerificationError) Error() string {
	if e == nil {
		return ErrVerificationRequired.Error()
	}

	if e.URL != "" {
		return fmt.Sprintf("%s: %s url=%s", ErrVerificationRequired, e.Reason, e.URL)
	}

	if e.Reason != "" {
		return fmt.Sprintf("%s: %s", ErrVerificationRequired, e.Reason)
	}

	return ErrVerificationRequired.Error()
}

func (e *VerificationError) Unwrap() error {
	return ErrVerificationRequired
}
