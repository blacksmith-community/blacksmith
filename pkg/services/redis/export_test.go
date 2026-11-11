package redis

import "github.com/redis/go-redis/v9"

// ClientOptions exposes clientOptions to the external test package.
func ClientOptions(creds *Credentials, useTLS bool) (*redis.Options, error) {
	return clientOptions(creds, useTLS)
}
