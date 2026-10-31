// Package auth provides password hashing, in-memory sessions and a login limiter.
package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

const (
	bcryptCost  = 12
	minPassword = 8
)

var ErrPasswordTooShort = errors.New("password must be at least 8 characters")

func HashPassword(pw string) (string, error) {
	if len(pw) < minPassword {
		return "", ErrPasswordTooShort
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}
