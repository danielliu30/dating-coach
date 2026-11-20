package auth

import (
	"fmt"
	"unicode/utf8"
)

const (
	MaxEmailLen       = 254
	MaxDisplayNameLen = 100
)

func validateIdentityLengths(email, displayName string) error {
	if utf8.RuneCountInString(email) > MaxEmailLen {
		return fmt.Errorf("%w: email must be at most %d characters", ErrInvalidInput, MaxEmailLen)
	}
	if utf8.RuneCountInString(displayName) > MaxDisplayNameLen {
		return fmt.Errorf("%w: display_name must be at most %d characters", ErrInvalidInput, MaxDisplayNameLen)
	}
	return nil
}
