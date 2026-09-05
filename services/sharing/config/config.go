package config

import (
	"github.com/spf13/viper"
)

// Config holds all configuration values for the sharing service.
type Config struct {
	ScyllaHosts             []string `mapstructure:"SCYLLA_HOSTS"`
	ScyllaPort              int      `mapstructure:"SCYLLA_PORT"`
	ScyllaDatacenter        string   `mapstructure:"SCYLLA_DATACENTER"`
	ScyllaReplicationFactor int      `mapstructure:"SCYLLA_REPLICATION_FACTOR"`
	ScyllaUsername          string   `mapstructure:"SCYLLA_USERNAME"`
	ScyllaPassword          string   `mapstructure:"SCYLLA_PASSWORD"`
	GRPCPort                string   `mapstructure:"SHARING_GRPC_PORT"`
	RedisURL                string   `mapstructure:"REDIS_URL"`
	MetadataURL             string   `mapstructure:"METADATA_URL"`
	AccessCacheTTLSeconds   int      `mapstructure:"ACCESS_CACHE_TTL_SECONDS"`
}

// Load reads configuration from environment variables (and optionally a .env file).
func Load() (Config, error) {
	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AddConfigPath("../..")
	_ = viper.ReadInConfig()

	viper.AutomaticEnv()

	viper.SetDefault("SHARING_GRPC_PORT", "50054")
	viper.SetDefault("SCYLLA_HOSTS", "localhost")
	viper.SetDefault("SCYLLA_PORT", 9042)
	viper.SetDefault("SCYLLA_DATACENTER", "datacenter1")
	viper.SetDefault("SCYLLA_REPLICATION_FACTOR", 1)
	viper.SetDefault("SCYLLA_USERNAME", "cassandra")
	viper.SetDefault("SCYLLA_PASSWORD", "cassandra")
	viper.SetDefault("REDIS_URL", "redis://localhost:6379")
	viper.SetDefault("METADATA_URL", "http://localhost:50053")
	viper.SetDefault("ACCESS_CACHE_TTL_SECONDS", 60)

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
