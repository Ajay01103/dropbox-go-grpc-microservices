package service

import (
	"fmt"

	"connectrpc.com/connect/v2"
)

func newConnectError(code connect.Code, message any) *connect.Error {
	return connect.NewError(code, fmt.Sprint(message))
}
