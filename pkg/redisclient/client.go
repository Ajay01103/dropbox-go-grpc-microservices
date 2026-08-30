package redisclient

import (
	"github.com/redis/go-redis/v9"
)

// NewClientFromURL parses the Redis URL and returns a connected client.
func NewClientFromURL(redisURL string) (*redis.Client, error) {
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	return redis.NewClient(opt), nil
}
