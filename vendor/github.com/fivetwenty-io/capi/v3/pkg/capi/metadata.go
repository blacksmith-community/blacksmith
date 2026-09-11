package capi

// metadataKey joins an optional prefix and name into a metadata key,
// e.g. ("example.com", "team") -> "example.com/team".
func metadataKey(prefix, name string) string {
	if prefix == "" {
		return name
	}

	return prefix + "/" + name
}

// SetLabel sets a label. An empty prefix applies the name as the key.
func (m *Metadata) SetLabel(prefix, name, value string) {
	if m.Labels == nil {
		m.Labels = map[string]*string{}
	}

	m.Labels[metadataKey(prefix, name)] = &value
}

// RemoveLabel marks a label for deletion: the key is kept with a nil
// value, which marshals to JSON null so the next update removes it
// server-side.
func (m *Metadata) RemoveLabel(prefix, name string) {
	if m.Labels == nil {
		m.Labels = map[string]*string{}
	}

	m.Labels[metadataKey(prefix, name)] = nil
}

// SetAnnotation sets an annotation. An empty prefix applies the name as
// the key.
func (m *Metadata) SetAnnotation(prefix, name, value string) {
	if m.Annotations == nil {
		m.Annotations = map[string]*string{}
	}

	m.Annotations[metadataKey(prefix, name)] = &value
}

// RemoveAnnotation marks an annotation for deletion: the key is kept
// with a nil value, which marshals to JSON null so the next update
// removes it server-side.
func (m *Metadata) RemoveAnnotation(prefix, name string) {
	if m.Annotations == nil {
		m.Annotations = map[string]*string{}
	}

	m.Annotations[metadataKey(prefix, name)] = nil
}

// StringValue dereferences a metadata value, returning "" for nil
// (a key marked for deletion).
func StringValue(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

// StringMap converts a plain string map into the pointer-valued form
// Metadata uses. Every entry is set; use nil values directly to mark
// keys for deletion.
func StringMap(values map[string]string) map[string]*string {
	if values == nil {
		return nil
	}

	converted := make(map[string]*string, len(values))
	for key, value := range values {
		converted[key] = &value
	}

	return converted
}
