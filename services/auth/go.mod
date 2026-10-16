module github.com/Ajay01103/go-dropbox/auth

go 1.27.0

require (
	connectrpc.com/connect/v2 v2.0.0
	github.com/Ajay01103/go-dropbox/pkg v0.0.0-00010101000000-000000000000
	github.com/dgraph-io/ristretto v0.2.0
	github.com/gocql/gocql v1.7.0
	github.com/google/uuid v1.6.0
	github.com/scylladb/gocqlx/v2 v2.8.0
	github.com/spf13/viper v1.21.0
	go.uber.org/zap v1.28.0
	golang.org/x/crypto v0.58.0
	google.golang.org/protobuf v1.36.12
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.1 // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goccy/go-json v0.11.2 // indirect
	github.com/golang-jwt/jwt/v5 v5.3.1 // indirect
	github.com/klauspost/compress v1.20.1 // indirect
	github.com/lestrrat-go/blackmagic v1.0.4 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/httprc v1.0.6 // indirect
	github.com/lestrrat-go/iter v1.0.2 // indirect
	github.com/lestrrat-go/jwx/v2 v2.1.7 // indirect
	github.com/lestrrat-go/option v1.0.1 // indirect
	github.com/pelletier/go-toml/v2 v2.4.3 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/rogpeppe/go-internal v1.16.0 // indirect
	github.com/sagikazarmark/locafero v0.12.0 // indirect
	github.com/scylladb/go-reflectx v1.0.1 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/spf13/afero v1.15.0 // indirect
	github.com/spf13/cast v1.10.0 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/subosito/gotenv v1.6.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
)

replace github.com/Ajay01103/go-dropbox/pkg => ../../pkg

replace github.com/gocql/gocql => github.com/scylladb/gocql v1.19.0
