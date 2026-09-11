package config

import (
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration values for the upload service.
type Config struct {
	ScyllaHosts                    []string      `mapstructure:"SCYLLA_HOSTS"`
	ScyllaPort                     int           `mapstructure:"SCYLLA_PORT"`
	ScyllaDatacenter               string        `mapstructure:"SCYLLA_DATACENTER"`
	ScyllaReplicationFactor        int           `mapstructure:"SCYLLA_REPLICATION_FACTOR"`
	ScyllaUsername                 string        `mapstructure:"SCYLLA_USERNAME"`
	ScyllaPassword                 string        `mapstructure:"SCYLLA_PASSWORD"`
	GRPCPort                       string        `mapstructure:"UPLOAD_GRPC_PORT"`
	JWKSURL                        string        `mapstructure:"JWKS_URL"`
	JWKSIssuer                     string        `mapstructure:"JWKS_ISSUER"`
	JWKSAudience                   []string      `mapstructure:"JWKS_AUDIENCE"`
	RedisURL                       string        `mapstructure:"REDIS_URL"`
	MetadataURL                    string        `mapstructure:"METADATA_URL"`
	NATSURL                        string        `mapstructure:"NATS_URL"`
	NATSEventSubject               string        `mapstructure:"NATS_EVENT_SUBJECT"`
	NATSBlockRefsRequestedSubject  string        `mapstructure:"NATS_BLOCK_REFS_REQUESTED_SUBJECT"`
	NATSBlockRefsCompletedSubject  string        `mapstructure:"NATS_BLOCK_REFS_COMPLETED_SUBJECT"`
	BlockLedgerStaleClaimThreshold time.Duration `mapstructure:"BLOCK_LEDGER_STALE_CLAIM_THRESHOLD"`
	ChunkSizeBytes                 int64         `mapstructure:"CHUNK_SIZE_BYTES"`
	UploadStoragePath              string        `mapstructure:"UPLOAD_STORAGE_PATH"`
	BlockSizeBytes                 int64         `mapstructure:"BLOCK_SIZE_BYTES"`
	S3Bucket                       string        `mapstructure:"S3_BUCKET"`
	S3Region                       string        `mapstructure:"S3_REGION"`
	S3Endpoint                     string        `mapstructure:"S3_ENDPOINT"`
	S3AccessKey                    string        `mapstructure:"AWS_ACCESS_KEY_ID"`
	S3SecretKey                    string        `mapstructure:"AWS_SECRET_ACCESS_KEY"`
	S3StorageBackend               string        `mapstructure:"S3_STORAGE_BACKEND"`
	SessionTTLSeconds              int64         `mapstructure:"SESSION_TTL_SECONDS"`
}

// Load reads configuration from environment variables (and optionally a .env file).
func Load() (Config, error) {
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AddConfigPath("../..")
	_ = viper.ReadInConfig()

	viper.AutomaticEnv()

	// Defaults
	viper.SetDefault("UPLOAD_GRPC_PORT", "50052")
	viper.SetDefault("JWKS_URL", "http://localhost:50051/.well-known/jwks.json")
	viper.SetDefault("JWKS_ISSUER", "")
	viper.SetDefault("JWKS_AUDIENCE", []string{})
	viper.SetDefault("SCYLLA_HOSTS", "localhost")
	viper.SetDefault("SCYLLA_PORT", 9042)
	viper.SetDefault("SCYLLA_DATACENTER", "datacenter1")
	viper.SetDefault("SCYLLA_REPLICATION_FACTOR", 1)
	viper.SetDefault("SCYLLA_USERNAME", "cassandra")
	viper.SetDefault("SCYLLA_PASSWORD", "cassandra")
	viper.SetDefault("REDIS_URL", "redis://localhost:6379")
	viper.SetDefault("METADATA_URL", "http://localhost:50053")
	viper.SetDefault("NATS_URL", "nats://localhost:4222")
	viper.SetDefault("NATS_EVENT_SUBJECT", "uploads.object.stored")
	viper.SetDefault("NATS_BLOCK_REFS_REQUESTED_SUBJECT", "blocks.refs.decrement.requested")
	viper.SetDefault("NATS_BLOCK_REFS_COMPLETED_SUBJECT", "blocks.refs.decrement.completed")
	viper.SetDefault("BLOCK_LEDGER_STALE_CLAIM_THRESHOLD", 15*time.Minute)
	viper.SetDefault("CHUNK_SIZE_BYTES", 4194304) // 4 MiB content-addressed blocks
	viper.SetDefault("BLOCK_SIZE_BYTES", 4194304) // 4 MiB
	viper.SetDefault("S3_BUCKET", "uploads")
	viper.SetDefault("S3_REGION", "us-east-1")
	viper.SetDefault("S3_ENDPOINT", "")
	viper.SetDefault("S3_STORAGE_BACKEND", "s3")
	viper.SetDefault("UPLOAD_STORAGE_PATH", "./uploads")
	viper.SetDefault("SESSION_TTL_SECONDS", 86400) // 24 hours

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return cfg, err
	}
	if cfg.BlockSizeBytes > 0 {
		cfg.ChunkSizeBytes = cfg.BlockSizeBytes
	}
	return cfg, nil
}
