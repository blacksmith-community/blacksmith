package bosh

import (
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

// The limits on a log bundle. The director builds the bundle, so Blacksmith
// treats every part of it as untrusted input.
const (
	// maxLogFileSize is the most of one file's content Blacksmith keeps.
	maxLogFileSize = 10 * 1024 * 1024

	// maxLogRequestBytes is how much file content one request may keep
	// across all the bundles it fetches.
	maxLogRequestBytes = 64 * 1024 * 1024

	// maxLogReadBytes is how much decompressed tar data Blacksmith reads
	// from one bundle before it stops, whatever the data holds.
	maxLogReadBytes = 512 * 1024 * 1024

	// maxLogNameBytes is the longest file name Blacksmith keeps whole.
	maxLogNameBytes = 256

	// maxLogFiles is the most files one bundle keeps, so a bundle of many
	// tiny entries cannot grow the map without bound.
	maxLogFiles = 5000

	// maxSkippedLogPaths is the most skipped paths a bundle lists. The
	// count of skipped files is exact.
	maxSkippedLogPaths = 1000

	// logNameCutMarker ends a file name that was cut to maxLogNameBytes.
	logNameCutMarker = "...[cut]"
)

// ErrNoLogBlob is returned when a fetch task is done and its record names no
// blob, so there is no bundle to download.
var ErrNoLogBlob = errors.New("the log fetch task finished without naming a log bundle")

// errLogReadLimit is what the counting reader returns once a bundle has
// held more than maxLogReadBytes of decompressed data.
var errLogReadLimit = errors.New("the log bundle holds more decompressed data than Blacksmith reads")

// LogBudget is what one request may still keep of the log bundles it
// fetches. A request that fetches the logs of several instances hands every
// FetchLogs call the same budget, so the request as a whole stays under the
// limit instead of each call having a limit of its own.
//
// Each kept file is charged at the size its name and content take once
// encoded as JSON, because the response is written as JSON, and encoding/json
// writes a control byte or a <, >, or & as a six-byte \u00XX escape. A log
// full of terminal colour codes would otherwise take six times its budget in
// the response. A bundle charges the budget only for the files it returns,
// so a bundle that fails partway charges nothing. It is not safe for
// concurrent use.
type LogBudget struct {
	// Remaining is how many bytes of encoded JSON the request may still keep.
	Remaining int64
}

// NewLogBudget returns the budget of a new request, which is 64 MiB.
func NewLogBudget() *LogBudget {
	return &LogBudget{Remaining: maxLogRequestBytes}
}

// LogBundle is the log files of one instance, as its director's log bundle
// holds them.
type LogBundle struct {
	// Files maps a file's path inside the archive to its content. It is
	// never nil. A name longer than 256 bytes is cut to 256 bytes and ends
	// with "...[cut]".
	Files map[string]string
	// Skipped lists the log files the bundle held and Files does not, such
	// as a file that would overrun the request's budget. It lists at most
	// 1000 paths.
	Skipped []string
	// SkippedCount is how many log files were skipped, which may be more
	// than Skipped lists.
	SkippedCount int
	// Cut lists the log files Files holds only the first 10 MiB of,
	// because the file was larger. Like Skipped, it lists at most 1000
	// paths, though the 512 MiB read limit ends a bundle long before it
	// could cut that many.
	Cut []string
	// CutCount is how many log files were cut, which may be more than Cut
	// lists.
	CutCount int
	// StoppedEarly is true when the read of the bundle stopped at the limit
	// on decompressed data, so files past that point were never seen.
	StoppedEarly bool
}

// skip records a log file the bundle held and Files does not.
func (b *LogBundle) skip(name string) {
	b.SkippedCount++

	if len(b.Skipped) < maxSkippedLogPaths {
		b.Skipped = append(b.Skipped, name)
	}
}

// cut records a log file Files holds only the first maxLogFileSize bytes of.
func (b *LogBundle) cut(name string) {
	b.CutCount++

	if len(b.Cut) < maxSkippedLogPaths {
		b.Cut = append(b.Cut, name)
	}
}

// logFileCharge is what a kept file takes of the request's budget: its name
// and content as JSON strings, with the colon after the name and the comma
// after the content.
func logFileCharge(name, content string) int64 {
	return jsonStringSize(name) + jsonStringSize(content) + int64(len(":,"))
}

// jsonStringSize is how many bytes encoding/json writes for text as a string,
// quotes included. It counts as the encoder writes: a control byte or a <,
// >, or & is a six-byte \u00XX escape, while a backslash, a quote, and the
// control bytes with a short escape such as \n take two bytes. Each byte that
// is not part of valid UTF-8 becomes U+FFFD, which takes three. U+2028 and
// U+2029 are escaped in six. Every other byte is written as it is.
func jsonStringSize(text string) int64 {
	size := int64(len(`""`))

	for index := 0; index < len(text); {
		char := text[index]
		if char < utf8.RuneSelf {
			size += jsonByteSize(char)
			index++

			continue
		}

		r, width := utf8.DecodeRuneInString(text[index:])

		switch {
		case r == utf8.RuneError && width == 1:
			size += int64(utf8.RuneLen(utf8.RuneError))
		case r == '\u2028' || r == '\u2029':
			size += int64(len(`\u2028`))
		default:
			size += int64(width)
		}

		index += width
	}

	return size
}

// jsonByteSize is how many bytes encoding/json writes for one ASCII byte of
// a string.
func jsonByteSize(char byte) int64 {
	switch char {
	case '"', '\\', '\b', '\f', '\n', '\r', '\t':
		return int64(len(`\n`))
	case '<', '>', '&':
		return int64(len(`\u003c`))
	}

	if char < ' ' {
		return int64(len(`\u0000`))
	}

	return 1
}

// noLogBlobError is a log fetch task that is done and names no blob.
type noLogBlobError struct {
	director   string
	deployment string
	job        string
	instance   string
	taskID     int
}

func (e *noLogBlobError) Error() string {
	return fmt.Sprintf("FetchLogs for instance %s/%s of deployment %s on BOSH director %s failed, because BOSH task %d finished in state done "+
		"and its record names no log bundle: %v. Check the task at /b/tasks/%d",
		e.job, e.instance, e.deployment, e.director, e.taskID, ErrNoLogBlob, e.taskID)
}

func (e *noLogBlobError) Unwrap() error {
	return ErrNoLogBlob
}

// logReadLimiter reads at most limit bytes of the decompressed bundle. Past
// the limit it returns errLogReadLimit, unless the data has ended there, so
// a bundle of exactly the limit still reads to its end.
type logReadLimiter struct {
	reader    io.Reader
	remaining int64
}

func (l *logReadLimiter) Read(buf []byte) (int, error) {
	if len(buf) == 0 {
		return 0, nil
	}

	if l.remaining <= 0 {
		var probe [1]byte

		count, err := l.reader.Read(probe[:])
		if count > 0 {
			return 0, errLogReadLimit
		}

		return 0, err //nolint:wrapcheck // the reader's own end of data or failure
	}

	if int64(len(buf)) > l.remaining {
		buf = buf[:l.remaining]
	}

	count, err := l.reader.Read(buf)
	l.remaining -= int64(count)

	return count, err //nolint:wrapcheck // the reader's own end of data or failure
}

// cutLogName cuts a name longer than maxLogNameBytes so that, with the cut
// marker, it is maxLogNameBytes long or less, and never splits a character.
func cutLogName(name string) string {
	if len(name) <= maxLogNameBytes {
		return name
	}

	keep := maxLogNameBytes - len(logNameCutMarker)
	for keep > 0 && !utf8.RuneStart(name[keep]) {
		keep--
	}

	return name[:keep] + logNameCutMarker
}
