package capi

// DeleteOption configures a delete operation.
type DeleteOption func(*DeleteOptions)

// DeleteOptions holds the resolved configuration for a delete operation.
type DeleteOptions struct {
	// Purge bypasses the service broker and forcibly removes the record from
	// the database. Defaults to false (normal delete flow).
	Purge bool
}

// WithPurge sets whether the delete operation should purge the resource.
// When true, the CAPI ?purge=true query parameter is sent, which bypasses
// the service broker and forcibly removes the record from the database.
func WithPurge(purge bool) DeleteOption {
	return func(o *DeleteOptions) {
		o.Purge = purge
	}
}

// ApplyDeleteOptions applies a slice of DeleteOption to a new DeleteOptions
// struct and returns the resolved configuration.
func ApplyDeleteOptions(opts []DeleteOption) DeleteOptions {
	o := DeleteOptions{}
	for _, opt := range opts {
		opt(&o)
	}

	return o
}
