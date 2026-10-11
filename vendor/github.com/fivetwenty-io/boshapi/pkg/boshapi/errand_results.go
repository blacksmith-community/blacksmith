package boshapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrandResult is one errand result as the director writes it in the task
// output of an errand run. A run on several instances writes one result per
// instance.
type ErrandResult struct {
	// ExitCode is the exit code of the errand, or nil when the director
	// reported none, which is the case for a missing exit_code key and for
	// null. A nil code is not a success, so check for nil before reading it.
	ExitCode   *int           `json:"exit_code"`
	Stdout     string         `json:"stdout"`
	Stderr     string         `json:"stderr"`
	Logs       ErrandLogs     `json:"logs"`
	ErrandName string         `json:"errand_name"`
	Instance   ErrandInstance `json:"instance"`
}

// ErrandLogs locates the log bundle an errand run saved in the director's
// blobstore. Both fields are empty when no bundle was saved.
type ErrandLogs struct {
	BlobstoreID string `json:"blobstore_id"`
	SHA1        string `json:"sha1"`
}

// ParseErrandResults reads the task output of an errand run. The director
// writes the results as a stream of JSON objects, not as an array, and
// separates them with a newline or with nothing, so the output is decoded
// value by value until it ends. A decode error names the value's number,
// counting from 1, and the byte offset where it starts, counting from 0.
func ParseErrandResults(output string) ([]ErrandResult, error) {
	results := make([]ErrandResult, 0)
	decoder := json.NewDecoder(strings.NewReader(output))

	for number := 1; ; number++ {
		start := skipSpace(output, int(decoder.InputOffset()))

		var raw json.RawMessage

		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return results, nil
			}

			return nil, errandDecodeError(number, start, output, err)
		}

		var result ErrandResult

		if err := decodeObject(string(raw), &result); err != nil {
			return nil, errandDecodeError(number, start, output, err)
		}

		results = append(results, result)
	}
}

// skipSpace returns the offset of the first byte at or after from that is not
// JSON whitespace, which is where the decoder's next value starts.
func skipSpace(output string, from int) int {
	for from < len(output) {
		switch output[from] {
		case ' ', '\t', '\n', '\r':
			from++
		default:
			return from
		}
	}

	return from
}

func errandDecodeError(number, start int, output string, err error) error {
	return fmt.Errorf("decoding errand results at value %d (byte offset %d) of the task output "+
		"(the value starts %q): %w; check that the output is the result of an errand run task, "+
		"that the task finished, and that each result is one complete JSON object",
		number, start, excerpt(output[start:]), err)
}
