package certinventory

import "errors"

// ErrSaveUnconfirmed means Blacksmith could not confirm that a record it
// must hold before a certificate mutation reached Vault, so the mutation
// is not sent. The upgrade package declares the same value under its own
// name, so errors.Is matches it through either.
var ErrSaveUnconfirmed = errors.New("save not confirmed")
