module github.com/Ajay01103/go-dropbox/sharing

go 1.25.0

require (
	connectrpc.com/connect v1.19.1
	github.com/Ajay01103/go-dropbox/pkg v0.0.0-00010101000000-000000000000
	github.com/dgraph-io/ristretto v0.1.1
	github.com/gocql/gocql v1.7.0
	github.com/google/uuid v1.6.0
	github.com/scylladb/gocqlx/v2 v2.8.0
	github.com/spf13/viper v1.21.0
	go.uber.org/zap v1.27.1
	golang.org/x/net v0.52.0
	google.golang.org/protobuf v1.36.11
)

replace github.com/Ajay01103/go-dropbox/pkg => ../../pkg
