package token

import "sync"

var accessClaimsPool = sync.Pool{
	New: func() any {
		return &AccessTokenClaims{}
	},
}

var refreshClaimsPool = sync.Pool{
	New: func() any {
		return &RefreshTokenClaims{}
	},
}
