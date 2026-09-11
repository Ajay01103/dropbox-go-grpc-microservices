package config

import (
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration values for the metadata service
type Config struct {
	ScyllaHosts                   []string      `mapstructure:"SCYLLA_HOSTS"`
	ScyllaPort                    int           `mapstructure:"SCYLLA_PORT"`
	ScyllaDatacenter              string        `mapstructure:"SCYLLA_DATACENTER"`
	ScyllaReplicationFactor       int           `mapstructure:"SCYLLA_REPLICATION_FACTOR"`
	ScyllaUsername                string        `mapstructure:"SCYLLA_USERNAME"`
	ScyllaPassword                string        `mapstructure:"SCYLLA_PASSWORD"`
	GRPCPort                      string        `mapstructure:"METADATA_GRPC_PORT"`
	JWKSURL                       string        `mapstructure:"JWKS_URL"`
	JWKSIssuer                    string        `mapstructure:"JWKS_ISSUER"`
	JWKSAudience                  []string      `mapstructure:"JWKS_AUDIENCE"`
	NATSURL                       string        `mapstructure:"NATS_URL"`
	NATSEventSubject              string        `mapstructure:"NATS_EVENT_SUBJECT"`
	NATSBlockRefsRequestedSubject string        `mapstructure:"NATS_BLOCK_REFS_REQUESTED_SUBJECT"`
	NATSBlockRefsCompletedSubject string        `mapstructure:"NATS_BLOCK_REFS_COMPLETED_SUBJECT"`
	PurgeMaxAttempts              int           `mapstructure:"PURGE_MAX_ATTEMPTS"`
	PurgeInitialRetryDelay        time.Duration `mapstructure:"PURGE_INITIAL_RETRY_DELAY"`
	PurgeMaxRetryDelay            time.Duration `mapstructure:"PURGE_MAX_RETRY_DELAY"`
	PurgeRetryBackoffMultiplier   float64       `mapstructure:"PURGE_RETRY_BACKOFF_MULTIPLIER"`
	PurgeRequestTimeout           time.Duration `mapstructure:"PURGE_REQUEST_TIMEOUT"`
	PurgeCompletionTimeout        time.Duration `mapstructure:"PURGE_COMPLETION_TIMEOUT"`
	PurgeStaleJobThreshold        time.Duration `mapstructure:"PURGE_STALE_JOB_THRESHOLD"`
	PurgeRecoveryInterval         time.Duration `mapstructure:"PURGE_RECOVERY_INTERVAL"`
	ThumbnailStoragePath          string        `mapstructure:"THUMBNAIL_STORAGE_PATH"`
	S3Bucket                      string        `mapstructure:"S3_BUCKET"`
	S3Region                      string        `mapstructure:"S3_REGION"`
	S3Endpoint                    string        `mapstructure:"S3_ENDPOINT"`
	S3AccessKey                   string        `mapstructure:"AWS_ACCESS_KEY_ID"`
	S3SecretKey                   string        `mapstructure:"AWS_SECRET_ACCESS_KEY"`
}

// Load reads configuration from environment variables (and optionally a .env file)
func Load() (Config, error) {
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AddConfigPath("../..")
	_ = viper.ReadInConfig()

	viper.AutomaticEnv()

	// Defaults
	viper.SetDefault("METADATA_GRPC_PORT", "50053")
	viper.SetDefault("JWKS_URL", "http://localhost:50051/.well-known/jwks.json")
	viper.SetDefault("JWKS_ISSUER", "")
	viper.SetDefault("JWKS_AUDIENCE", []string{})
	viper.SetDefault("NATS_URL", "nats://localhost:4222")
	viper.SetDefault("NATS_EVENT_SUBJECT", "uploads.object.stored")
	viper.SetDefault("NATS_BLOCK_REFS_REQUESTED_SUBJECT", "blocks.refs.decrement.requested")
	viper.SetDefault("NATS_BLOCK_REFS_COMPLETED_SUBJECT", "blocks.refs.decrement.completed")
	viper.SetDefault("PURGE_MAX_ATTEMPTS", 10)
	viper.SetDefault("PURGE_INITIAL_RETRY_DELAY", time.Minute)
	viper.SetDefault("PURGE_MAX_RETRY_DELAY", time.Hour)
	viper.SetDefault("PURGE_RETRY_BACKOFF_MULTIPLIER", 2.0)
	viper.SetDefault("PURGE_REQUEST_TIMEOUT", 10*time.Second)
	viper.SetDefault("PURGE_COMPLETION_TIMEOUT", 15*time.Minute)
	viper.SetDefault("PURGE_STALE_JOB_THRESHOLD", time.Hour)
	viper.SetDefault("PURGE_RECOVERY_INTERVAL", time.Minute)
	viper.SetDefault("THUMBNAIL_STORAGE_PATH", "../upload/uploads")
	viper.SetDefault("S3_BUCKET", "uploads")
	viper.SetDefault("S3_REGION", "us-east-1")
	viper.SetDefault("SCYLLA_HOSTS", "localhost")
	viper.SetDefault("SCYLLA_PORT", 9042)
	viper.SetDefault("SCYLLA_DATACENTER", "datacenter1")
	viper.SetDefault("SCYLLA_REPLICATION_FACTOR", 1)
	viper.SetDefault("SCYLLA_USERNAME", "cassandra")
	viper.SetDefault("SCYLLA_PASSWORD", "cassandra")

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
