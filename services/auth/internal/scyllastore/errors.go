package scyllastore

import "errors"

// Common errors for token store operations
var (
	ErrTokenHashBlacklisted = errors.New("token hash is blacklisted")
	ErrFamilyAlreadyExists  = errors.New("token family already exists")
)
