package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      = 19456
	argonIterations  = 2
	argonParallelism = 1
	argonKeyLength   = 32
)

func ValidateNewPassword(password string) error {
	if len(password) > 1024 || utf8RuneCount(password) < 13 {
		return ErrPasswordPolicy
	}
	var upper, number, symbol bool
	for _, r := range password {
		upper = upper || unicode.IsUpper(r)
		number = number || unicode.IsNumber(r)
		symbol = symbol || unicode.IsPunct(r) || unicode.IsSymbol(r)
	}
	if !upper || !number || !symbol {
		return ErrPasswordPolicy
	}
	return nil
}

func utf8RuneCount(value string) int {
	return len([]rune(value))
}

func HashPassword(password string) (string, error) {
	if len(password) > 1024 {
		return "", ErrPasswordPolicy
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonIterations, argonParallelism, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errors.New("invalid password hash")
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, errors.New("invalid password hash")
	}
	values := map[string]uint64{}
	for _, parameter := range params {
		key, value, ok := strings.Cut(parameter, "=")
		if !ok {
			return false, errors.New("invalid password hash")
		}
		parsed, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return false, errors.New("invalid password hash")
		}
		values[key] = parsed
	}
	memory, iterations, parallelism := values["m"], values["t"], values["p"]
	if memory == 0 || memory > 65536 || iterations == 0 || iterations > 8 || parallelism == 0 || parallelism > 4 {
		return false, errors.New("unsupported password hash")
	}
	salt, err := decodePasswordPart(parts[4])
	if err != nil || len(salt) < 8 {
		return false, errors.New("invalid password hash")
	}
	want, err := decodePasswordPart(parts[5])
	if err != nil || len(want) < 16 || len(want) > 64 {
		return false, errors.New("invalid password hash")
	}
	got := argon2.IDKey([]byte(password), salt, uint32(iterations), uint32(memory), uint8(parallelism), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func decodePasswordPart(value string) ([]byte, error) {
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	return base64.StdEncoding.DecodeString(value)
}
