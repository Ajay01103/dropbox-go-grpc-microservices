package config

import (
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration values loaded from environment variables.
type Config struct {
	ScyllaHosts                      []string      `mapstructure:"SCYLLA_HOSTS"`
	ScyllaPort                       int           `mapstructure:"SCYLLA_PORT"`
	ScyllaDatacenter                 string        `mapstructure:"SCYLLA_DATACENTER"`
	ScyllaReplicationFactor          int           `mapstructure:"SCYLLA_REPLICATION_FACTOR"`
	ScyllaUsername                   string        `mapstructure:"SCYLLA_USERNAME"`
	ScyllaPassword                   string        `mapstructure:"SCYLLA_PASSWORD"`
	JWTSecret                        string        `mapstructure:"JWT_SECRET"`
	GRPCPort                         string        `mapstructure:"AUTH_GRPC_PORT"`
	AccessTokenDuration              time.Duration `mapstructure:"ACCESS_TOKEN_DURATION"`
	RefreshTokenDuration             time.Duration `mapstructure:"REFRESH_TOKEN_DURATION"`
	EDDSASigningKeyRetentionDuration time.Duration `mapstructure:"EDDSA_SIGNING_KEY_RETENTION_DURATION"`
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
	viper.SetDefault("AUTH_GRPC_PORT", "50051")
	viper.SetDefault("SCYLLA_HOSTS", "localhost")
	viper.SetDefault("SCYLLA_PORT", 9042)
	viper.SetDefault("SCYLLA_DATACENTER", "datacenter1")
	viper.SetDefault("SCYLLA_REPLICATION_FACTOR", 1)
	viper.SetDefault("SCYLLA_USERNAME", "cassandra")
	viper.SetDefault("SCYLLA_PASSWORD", "cassandra")
	viper.SetDefault("ACCESS_TOKEN_DURATION", "15m")
	viper.SetDefault("REFRESH_TOKEN_DURATION", "168h")
	viper.SetDefault("EDDSA_SIGNING_KEY_RETENTION_DURATION", "336h")

	// Backward-compatible fallback for older GRPC_PORT env var.
	if !viper.IsSet("AUTH_GRPC_PORT") && viper.IsSet("GRPC_PORT") {
		viper.Set("AUTH_GRPC_PORT", viper.GetString("GRPC_PORT"))
	}

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
