package response

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// HandleJSON writes a JSON response to the HTTP response writer.
// If err is not nil, it writes an error response with status 500.
// If result marshals successfully, it writes the result with status 200.
// If marshaling fails, it writes an error response with status 500.
func HandleJSON(writer http.ResponseWriter, result interface{}, err error) {
	writer.Header().Set("Content-Type", "application/json")

	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)

		errorResponse := map[string]interface{}{
			"success": false,
			"error":   err.Error(),
		}

		if jsonData, jsonErr := json.Marshal(errorResponse); jsonErr == nil {
			_, _ = writer.Write(jsonData)
		} else {
			_, _ = fmt.Fprintf(writer, `{"success": false, "error": "internal error"}`)
		}

		return
	}

	if jsonData, jsonErr := json.Marshal(result); jsonErr == nil {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write(jsonData)
	} else {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(writer, `{"success": false, "error": "failed to marshal response"}`)
	}
}

// WriteError writes a JSON error response with the specified HTTP status code.
func WriteError(writer http.ResponseWriter, statusCode int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)

	errorResponse := map[string]interface{}{
		"success": false,
		"error":   message,
	}

	if jsonData, err := json.Marshal(errorResponse); err == nil {
		_, _ = writer.Write(jsonData)
	} else {
		_, _ = fmt.Fprintf(writer, `{"success": false, "error": "internal error"}`)
	}
}

// WriteSuccess writes a JSON success response with the specified data.
func WriteSuccess(writer http.ResponseWriter, data interface{}) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)

	if jsonData, err := json.Marshal(data); err == nil {
		_, _ = writer.Write(jsonData)
	} else {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(writer, `{"success": false, "error": "failed to marshal response"}`)
	}
}

// ParseJSON parses JSON from an io.Reader into the provided target interface.
func ParseJSON(reader io.Reader, target interface{}) error {
	decoder := json.NewDecoder(reader)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("failed to decode JSON: %w", err)
	}

	return nil
}
