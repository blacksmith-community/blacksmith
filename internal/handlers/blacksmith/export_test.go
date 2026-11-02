package blacksmith

// FormatManifestResponse exposes the manifest answer the UI reads, which holds
// the manifest text and its parsed form.
func (h *Handler) FormatManifestResponse(manifestYAML string) map[string]interface{} {
	return h.formatManifestResponse(manifestYAML)
}
