package config

import (
	"github.com/spf13/viper"
)

// Config holds all configuration values for the upload service.
type Config struct {
	ScyllaHosts             []string `mapstructure:"SCYLLA_HOSTS"`
	ScyllaPort              int      `mapstructure:"SCYLLA_PORT"`
	ScyllaDatacenter        string   `mapstructure:"SCYLLA_DATACENTER"`
	ScyllaReplicationFactor int      `mapstructure:"SCYLLA_REPLICATION_FACTOR"`
	ScyllaUsername          string   `mapstructure:"SCYLLA_USERNAME"`
	ScyllaPassword          string   `mapstructure:"SCYLLA_PASSWORD"`
	GRPCPort                string   `mapstructure:"UPLOAD_GRPC_PORT"`
	RedisURL                string   `mapstructure:"REDIS_URL"`
	ChunkSizeBytes          int64    `mapstructure:"CHUNK_SIZE_BYTES"`
	UploadStoragePath       string   `mapstructure:"UPLOAD_STORAGE_PATH"`
	SessionTTLSeconds       int64    `mapstructure:"SESSION_TTL_SECONDS"`
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
	viper.SetDefault("SCYLLA_HOSTS", "localhost")
	viper.SetDefault("SCYLLA_PORT", 9042)
	viper.SetDefault("SCYLLA_DATACENTER", "datacenter1")
	viper.SetDefault("SCYLLA_REPLICATION_FACTOR", 1)
	viper.SetDefault("SCYLLA_USERNAME", "cassandra")
	viper.SetDefault("SCYLLA_PASSWORD", "cassandra")
	viper.SetDefault("REDIS_URL", "redis://localhost:6379")
	viper.SetDefault("CHUNK_SIZE_BYTES", 33554432) // 32 MB
	viper.SetDefault("UPLOAD_STORAGE_PATH", "./uploads")
	viper.SetDefault("SESSION_TTL_SECONDS", 86400) // 24 hours

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
