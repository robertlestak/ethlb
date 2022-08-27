package cache

import (
	"fmt"
	"os"
	"time"

	"github.com/go-redis/redis"

	log "github.com/sirupsen/logrus"
)

var (
	Client *redis.Client
)

const (
	cachePrefix = "cache:"
)

func Init() error {
	l := log.WithFields(log.Fields{
		"package": "cache",
	})
	l.Debug("Initializing redis client")
	Client = redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%s", os.Getenv("REDIS_HOST"), os.Getenv("REDIS_PORT")),
		Password: "", // no password set
		DB:       0,  // use default DB
	})
	cmd := Client.Ping()
	if cmd.Err() != nil {
		l.Error("Failed to connect to redis")
		return cmd.Err()
	}
	l.Debug("Connected to redis")
	return nil
}

func Get(key string) (string, error) {
	l := log.WithFields(log.Fields{
		"package": "cache",
	})
	l.Debug("Getting key from redis")
	cmd := Client.Get(cachePrefix + key)
	if cmd.Err() != nil && cmd.Err() != redis.Nil {
		l.Error("Failed to get key from redis")
		return "", cmd.Err()
	} else if cmd.Err() == redis.Nil {
		l.Debug("Key not found in redis")
		return "", nil
	}
	l.Debug("Got key from redis")
	return cmd.Result()
}

func Set(key string, value string, exp time.Duration) error {
	l := log.WithFields(log.Fields{
		"package": "cache",
	})
	l.Debug("Setting key in redis")
	cmd := Client.Set(cachePrefix+key, value, exp)
	if cmd.Err() != nil {
		l.Error("Failed to set key in redis")
		return cmd.Err()
	}
	l.Debug("Set key in redis")
	return nil
}
