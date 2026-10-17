package config

import (
	"time"

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
	JWKSURL                 string   `mapstructure:"JWKS_URL"`
	JWKSIssuer              string   `mapstructure:"JWKS_ISSUER"`
	JWKSAudience            []string `mapstructure:"JWKS_AUDIENCE"`
	NATSURL                 string   `mapstructure:"NATS_URL"`
	// Thumbnail sweeper (recovers lost v2 FileStored publishes).
	ThumbnailSweepInterval      time.Duration `mapstructure:"THUMBNAIL_SWEEP_INTERVAL"`
	ThumbnailSweepStaleAfter    time.Duration `mapstructure:"THUMBNAIL_SWEEP_STALE_AFTER"`
	PurgeMaxAttempts            int           `mapstructure:"PURGE_MAX_ATTEMPTS"`
	PurgeInitialRetryDelay      time.Duration `mapstructure:"PURGE_INITIAL_RETRY_DELAY"`
	PurgeMaxRetryDelay          time.Duration `mapstructure:"PURGE_MAX_RETRY_DELAY"`
	PurgeRetryBackoffMultiplier float64       `mapstructure:"PURGE_RETRY_BACKOFF_MULTIPLIER"`
	PurgeRequestTimeout         time.Duration `mapstructure:"PURGE_REQUEST_TIMEOUT"`
	PurgeCompletionTimeout      time.Duration `mapstructure:"PURGE_COMPLETION_TIMEOUT"`
	PurgeStaleJobThreshold      time.Duration `mapstructure:"PURGE_STALE_JOB_THRESHOLD"`
	PurgeRecoveryInterval       time.Duration `mapstructure:"PURGE_RECOVERY_INTERVAL"`
	ThumbnailStoragePath        string        `mapstructure:"THUMBNAIL_STORAGE_PATH"`
	S3Bucket                    string        `mapstructure:"S3_BUCKET"`
	S3Region                    string        `mapstructure:"S3_REGION"`
	S3Endpoint                  string        `mapstructure:"S3_ENDPOINT"`
	// S3PublicEndpoint is the object-store address clients can reach, used only
	// for presigning URLs (host is signed, so it cannot be rewritten). Empty
	// means the same as S3_ENDPOINT.
	S3PublicEndpoint string `mapstructure:"S3_PUBLIC_ENDPOINT"`
	S3AccessKey      string `mapstructure:"AWS_ACCESS_KEY_ID"`
	S3SecretKey      string `mapstructure:"AWS_SECRET_ACCESS_KEY"`
	// Download-ticket and content-serving settings. DownloadTicketKey must
	// be shared with the content server (it verifies statelessly).
	DownloadTicketKey string `mapstructure:"DOWNLOAD_TICKET_KEY"`
	ContentBaseURL    string `mapstructure:"CONTENT_BASE_URL"`
	// ContentPublicBaseURL is the content-server address handed to clients. In
	// dev the service answers on localhost, but a phone on the LAN needs the
	// machine's address. Empty means the same as CONTENT_BASE_URL.
	ContentPublicBaseURL string `mapstructure:"CONTENT_PUBLIC_BASE_URL"`
	// Tier B: async stitcher that consolidates per-block objects into one
	// object per file via UploadPartCopy (server-side copy, zero egress).
	StitchEnabled    bool          `mapstructure:"STITCH_ENABLED"`
	StitchInterval   time.Duration `mapstructure:"STITCH_INTERVAL"`
	StitchBatchSize  int           `mapstructure:"STITCH_BATCH_SIZE"`
	StitchMinAgeSecs int           `mapstructure:"STITCH_MIN_AGE_SECONDS"`
	// In-browser editing (PDF / DOCX / XLSX). One kill switch per type so a
	// bad round-trip can be turned off without touching data or code, and one
	// size cap per type because the editors hold the whole document in browser
	// memory: the cap is enforced before the file is loaded, not after.
	EditPDFEnabled   bool  `mapstructure:"PDF_EDIT_ENABLED"`
	EditDocxEnabled  bool  `mapstructure:"DOCX_EDIT_ENABLED"`
	EditXlsxEnabled  bool  `mapstructure:"XLSX_EDIT_ENABLED"`
	EditPDFMaxBytes  int64 `mapstructure:"EDIT_PDF_MAX_BYTES"`
	EditDocxMaxBytes int64 `mapstructure:"EDIT_DOCX_MAX_BYTES"`
	EditXlsxMaxBytes int64 `mapstructure:"EDIT_XLSX_MAX_BYTES"`
}

// ClientContentBaseURL is the base URL clients should fetch content from.
//
// It exists separately from ContentBaseURL because the two audiences differ: the
// service reaches itself over loopback, while a phone on the LAN (or a browser
// on another machine) needs an address it can resolve.
func (c Config) ClientContentBaseURL() string {
	if c.ContentPublicBaseURL != "" {
		return c.ContentPublicBaseURL
	}
	return c.ContentBaseURL
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
	viper.SetDefault("THUMBNAIL_SWEEP_INTERVAL", time.Minute)
	viper.SetDefault("THUMBNAIL_SWEEP_STALE_AFTER", 5*time.Minute)
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
	viper.SetDefault("DOWNLOAD_TICKET_KEY", "dev-ticket-key-change-me")
	// The /content route is mounted on this service's own HTTP port, so the
	// default base URL is the metadata port.
	viper.SetDefault("CONTENT_BASE_URL", "http://localhost:50053")
	viper.SetDefault("STITCH_ENABLED", false)
	viper.SetDefault("STITCH_INTERVAL", time.Minute)
	viper.SetDefault("STITCH_BATCH_SIZE", 5)
	viper.SetDefault("STITCH_MIN_AGE_SECONDS", 30)
	viper.SetDefault("PDF_EDIT_ENABLED", true)
	viper.SetDefault("DOCX_EDIT_ENABLED", true)
	viper.SetDefault("XLSX_EDIT_ENABLED", true)
	// Caps from the edit plan: PDF 50 MiB (the pdf worker's own ceiling is
	// 100 MiB), DOCX and XLSX 25 MiB each.
	viper.SetDefault("EDIT_PDF_MAX_BYTES", 50<<20)
	viper.SetDefault("EDIT_DOCX_MAX_BYTES", 25<<20)
	viper.SetDefault("EDIT_XLSX_MAX_BYTES", 25<<20)

	var cfg Config
	if err := viper.Unmarshal(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
