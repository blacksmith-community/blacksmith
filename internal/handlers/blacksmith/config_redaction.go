package blacksmith

// ConfigRedactedPlaceholder replaces every secret value in the config endpoint
// response. It avoids angle brackets because the admin UI renders values as HTML.
const ConfigRedactedPlaceholder = "(redacted)"

// secretConfigKeys lists the YAML keys that carry secret material anywhere in
// the config tree. The match is on the key name alone, so a new secret field
// that reuses one of these names is redacted without a handler change.
//
//nolint:gochecknoglobals // read-only lookup table
var secretConfigKeys = map[string]struct{}{
	"password":      {},
	"broker_pass":   {},
	"token":         {},
	"client_secret": {},
	"secret":        {},
	"key":           {},
}

// redactConfigSecrets walks the decoded config tree in place and replaces every
// non-empty value held under a secret key with ConfigRedactedPlaceholder. An
// empty value stays empty so the UI still shows which secrets are unset.
func redactConfigSecrets(node interface{}) {
	switch typed := node.(type) {
	case map[string]interface{}:
		for key, value := range typed {
			if _, secret := secretConfigKeys[key]; secret && !isEmptyConfigValue(value) {
				typed[key] = ConfigRedactedPlaceholder

				continue
			}

			redactConfigSecrets(value)
		}
	case []interface{}:
		for _, item := range typed {
			redactConfigSecrets(item)
		}
	}
}

func isEmptyConfigValue(value interface{}) bool {
	if value == nil {
		return true
	}

	text, isString := value.(string)

	return isString && text == ""
}
