package config

import (
	"github.com/spf13/viper"
)

// Config holds all configuration values for the metadata service
type Config struct {
	ScyllaHosts             []string `mapstructure:"SCYLLA_HOSTS"`
	ScyllaPort              int      `mapstructure:"SCYLLA_PORT"`
	ScyllaDatacenter        string   `mapstructure:"SCYLLA_DATACENTER"`
	ScyllaReplicationFactor int      `mapstructure:"SCYLLA_REPLICATION_FACTOR"`
	ScyllaUsername          string   `mapstructure:"SCYLLA_USERNAME"`
	ScyllaPassword          string   `mapstructure:"SCYLLA_PASSWORD"`
	GRPCPort                string   `mapstructure:"METADATA_GRPC_PORT"`
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
