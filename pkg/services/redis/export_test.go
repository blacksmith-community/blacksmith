package redis

import "github.com/go-redis/redis/v8"

// ClientOptions exposes clientOptions to the external test package.
func ClientOptions(creds *Credentials, useTLS bool) (*redis.Options, error) {
	return clientOptions(creds, useTLS)
}
