package database

import (
	"errors"
	"net/mail"
	"strings"
	"unicode/utf8"
)

const maxEmailLength = 254

// NormalizeEmail trims and lowercases an account email, then validates its
// format and Unicode character length.
func NormalizeEmail(email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || utf8.RuneCountInString(email) > maxEmailLength {
		return "", errors.New("邮箱长度必须为 1 至 254 个字符")
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email {
		return "", errors.New("邮箱格式无效")
	}
	return email, nil
}
