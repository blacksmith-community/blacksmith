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

		jsonData, jsonErr := json.Marshal(errorResponse)
		if jsonErr == nil {
			_, _ = writer.Write(jsonData)
		} else {
			_, _ = fmt.Fprintf(writer, `{"success": false, "error": "internal error"}`)
		}

		return
	}

	jsonData, jsonErr := json.Marshal(result)
	if jsonErr == nil {
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

	jsonData, err := json.Marshal(errorResponse)
	if err == nil {
		_, _ = writer.Write(jsonData)
	} else {
		_, _ = fmt.Fprintf(writer, `{"success": false, "error": "internal error"}`)
	}
}

// WriteSuccess writes a JSON success response with the specified data.
func WriteSuccess(writer http.ResponseWriter, data interface{}) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)

	jsonData, err := json.Marshal(data)
	if err == nil {
		_, _ = writer.Write(jsonData)
	} else {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(writer, `{"success": false, "error": "failed to marshal response"}`)
	}
}

// ParseJSON parses JSON from an io.Reader into the provided target interface.
func ParseJSON(reader io.Reader, target interface{}) error {
	decoder := json.NewDecoder(reader)

	err := decoder.Decode(target)
	if err != nil {
		return fmt.Errorf("failed to decode JSON: %w", err)
	}

	return nil
}

// JSON writes a JSON response with the given status code and data.
func JSON(writer http.ResponseWriter, statusCode int, data interface{}) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(statusCode)

	jsonData, err := json.Marshal(data)
	if err == nil {
		_, _ = writer.Write(jsonData)
	} else {
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(writer, `{"success": false, "error": "failed to marshal response"}`)
	}
}
