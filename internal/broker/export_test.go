package broker

import (
	"context"
	"time"

	"blacksmith/pkg/logger"
)

// CreateValkeyACLUser exposes the bind path's ACL user creation to package
// broker_test, running it against the node or nodes credMap describes.
func (b *Broker) CreateValkeyACLUser(ctx context.Context, instanceID string, credMap map[string]interface{}, username, password string, brokerLogger logger.Logger) error {
	conn, err := extractValkeyConnInfo(credMap)
	if err != nil {
		return err
	}

	return b.createACLUser(ctx, instanceID, conn, username, password, brokerLogger)
}

// DeleteValkeyACLUser exposes the unbind path's ACL user deletion to package
// broker_test, running it against the node or nodes credMap describes.
func (b *Broker) DeleteValkeyACLUser(ctx context.Context, instanceID string, credMap map[string]interface{}, username string, brokerLogger logger.Logger) error {
	conn, err := extractValkeyConnInfo(credMap)
	if err != nil {
		return err
	}

	return b.deleteACLUser(ctx, instanceID, conn, username, brokerLogger)
}

// SetValkeyACLRetry overrides the Valkey ACL retry settings and returns a
// function that restores the previous ones.
func SetValkeyACLRetry(retries int, baseWait time.Duration) func() {
	previousRetries, previousWait := valkeyACLRetries, valkeyACLBaseWait
	valkeyACLRetries, valkeyACLBaseWait = retries, baseWait

	return func() {
		valkeyACLRetries, valkeyACLBaseWait = previousRetries, previousWait
	}
}
