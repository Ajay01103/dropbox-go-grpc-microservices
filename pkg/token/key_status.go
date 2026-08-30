package token

type KeyStatus string

const (
	KeyStatusActive  KeyStatus = "active"
	KeyStatusRetired KeyStatus = "retired"
	KeyStatusRevoked KeyStatus = "revoked"
)
