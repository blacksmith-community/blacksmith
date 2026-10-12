package bosh_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"blacksmith/internal/bosh"
	"blacksmith/internal/bosh/boshtest"
	"github.com/fivetwenty-io/boshapi/pkg/boshapi"
)

// The instance and blob the log tests fetch.
const (
	logsJob      = "node"
	logsInstance = "uuid-0"
	logsBlob     = "blob-1"

	// logsStartPath is the route a log fetch is started on.
	logsStartPath = "/deployments/" + detailsDeployment + "/jobs/" + logsJob + "/" + logsInstance + "/logs"

	// logsBlobPath is the route the log bundle is downloaded from.
	logsBlobPath = "/resources/" + logsBlob

	// logsOperation is the operation a log fetch's errors name.
	logsOperation = "FetchLogs"

	mebibyte = 1024 * 1024

	// keptContent and firstContent are file contents the limit tests
	// expect a bundle to keep.
	keptContent  = "kept\n"
	firstContent = "first\n"
)

// logsConfig says how a logsDirector answers.
type logsConfig struct {
	// start answers the start of the log fetch. Nil redirects to the task.
	start func(http.ResponseWriter, *http.Request)
	// state returns the task's state and result for the nth read, counting
	// from 1. Nil answers done with the blob ID as the result.
	state func(poll int32, cancels int32) (string, string)
	// output is the task's result output, which a log fetch must not read.
	output string
	// bundle answers the download. Nil answers the director's 404.
	bundle func(http.ResponseWriter, *http.Request)
	// cancel answers a cancel of the task. Nil answers 204.
	cancel func(http.ResponseWriter, *http.Request)
}

// logsDirector is a test director that starts a log fetch as a task and
// serves the bundle the task produced.
type logsDirector struct {
	*boshtest.Director

	polls   atomic.Int32
	cancels atomic.Int32

	firstPoll     chan struct{}
	firstPollOnce sync.Once
}

func newLogsDirector(t *testing.T, cfg logsConfig) *logsDirector {
	t.Helper()

	director := &logsDirector{firstPoll: make(chan struct{})}

	director.Director = boshtest.New(t, func(writer http.ResponseWriter, request *http.Request) bool {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == logsStartPath:
			if cfg.start != nil {
				cfg.start(writer, request)

				return true
			}

			writer.Header().Set("Location", detailsTaskPath)
			writer.WriteHeader(http.StatusFound)
		case request.Method == http.MethodGet && request.URL.Path == detailsTaskPath:
			poll := director.polls.Add(1)
			director.firstPollOnce.Do(func() { close(director.firstPoll) })

			state, result := batchTaskDone, logsBlob
			if cfg.state != nil {
				state, result = cfg.state(poll, director.cancels.Load())
			}

			task := directorTask(detailsTaskID, state)
			task["description"] = "fetch logs"
			task["result"] = result
			task["deployment"] = detailsDeployment

			boshtest.WriteJSON(writer, http.StatusOK, task)
		case request.Method == http.MethodGet && request.URL.Path == detailsTaskPath+"/output":
			boshtest.WriteText(writer, http.StatusOK, cfg.output)
		case request.Method == http.MethodGet && request.URL.Path == logsBlobPath && cfg.bundle != nil:
			cfg.bundle(writer, request)
		case request.Method == http.MethodDelete && request.URL.Path == detailsCancelPath:
			director.cancels.Add(1)

			if cfg.cancel != nil {
				cfg.cancel(writer, request)

				return true
			}

			writer.WriteHeader(http.StatusNoContent)
		default:
			return false
		}

		return true
	})

	return director
}

// bundleFile is one entry of a test log bundle. A repeat above zero gives
// the entry that many bytes of text in place of body.
type bundleFile struct {
	name     string
	typeflag byte
	link     string
	body     string
	repeat   int64
}

func logFile(name, body string) bundleFile {
	return bundleFile{name: name, typeflag: tar.TypeReg, body: body}
}

func bigLogFile(name string, size int64) bundleFile {
	return bundleFile{name: name, typeflag: tar.TypeReg, repeat: size}
}

// bundleWriter writes a gzipped tar the way a director's blobstore serves a
// log bundle, entry by entry, so a handler can stream it.
type bundleWriter struct {
	gz   *gzip.Writer
	tw   *tar.Writer
	zero []byte
}

func newBundleWriter(t *testing.T, writer io.Writer, level int) *bundleWriter {
	t.Helper()

	gz, err := gzip.NewWriterLevel(writer, level)
	if err != nil {
		t.Fatalf("creating the gzip writer: %v", err)
	}

	return &bundleWriter{gz: gz, tw: tar.NewWriter(gz), zero: make([]byte, mebibyte)}
}

// fill writes size bytes of fill into the current entry, and stops with the
// write error once the reader has gone away.
func (b *bundleWriter) fill(size int64, fill byte, written *atomic.Int64) error {
	chunk := b.zero
	if fill != 0 {
		chunk = []byte(strings.Repeat(string([]byte{fill}), mebibyte))
	}

	for size > 0 {
		next := int64(len(chunk))
		if next > size {
			next = size
		}

		_, err := b.tw.Write(chunk[:next])
		if err != nil {
			return err //nolint:wrapcheck // the test only needs to know it failed
		}

		size -= next

		if written != nil {
			written.Add(next)
		}
	}

	return nil
}

func (b *bundleWriter) add(file bundleFile) error {
	size := int64(len(file.body))
	if file.repeat > 0 {
		size = file.repeat
	}

	header := &tar.Header{Name: file.name, Typeflag: file.typeflag, Linkname: file.link, Mode: 0o644}
	if file.typeflag == tar.TypeReg {
		header.Size = size
	}

	err := b.tw.WriteHeader(header)
	if err != nil {
		return err //nolint:wrapcheck // the test only needs to know it failed
	}

	if file.typeflag != tar.TypeReg {
		return nil
	}

	if file.repeat > 0 {
		return b.fill(file.repeat, 'x', nil)
	}

	_, err = b.tw.Write([]byte(file.body))

	return err //nolint:wrapcheck // the test only needs to know it failed
}

// padding starts a regular entry of size bytes that no log file keeps.
func (b *bundleWriter) padding(name string, size int64, written *atomic.Int64) error {
	err := b.tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: size, Mode: 0o644})
	if err != nil {
		return err //nolint:wrapcheck // the test only needs to know it failed
	}

	return b.fill(size, 0, written)
}

func (b *bundleWriter) close() {
	_ = b.tw.Close()
	_ = b.gz.Close()
}

// serveBundle answers the download with files.
func serveBundle(t *testing.T, files ...bundleFile) func(http.ResponseWriter, *http.Request) {
	t.Helper()

	return func(writer http.ResponseWriter, _ *http.Request) {
		bundle := newBundleWriter(t, writer, gzip.DefaultCompression)
		defer bundle.close()

		for _, file := range files {
			err := bundle.add(file)
			if err != nil {
				return
			}
		}
	}
}

func fetchLogs(t *testing.T, director bosh.Director, budget *bosh.LogBudget) (*bosh.LogBundle, error) {
	t.Helper()

	var bundle *bosh.LogBundle

	err := callWithin(t, 60*time.Second, func() error {
		var fetchErr error

		bundle, fetchErr = director.FetchLogs(t.Context(), detailsDeployment, logsJob, logsInstance, budget)

		return fetchErr //nolint:wrapcheck // the test asserts on the adapter's own error
	})

	return bundle, err
}

func mustFetchLogs(t *testing.T, director bosh.Director, budget *bosh.LogBudget) *bosh.LogBundle {
	t.Helper()

	bundle, err := fetchLogs(t, director, budget)
	if err != nil {
		t.Fatalf("expected the log fetch to succeed, got: %v", err)
	}

	if bundle == nil {
		t.Fatal("the log fetch returned no bundle and no error")
	}

	return bundle
}

// The fetch starts the task for the instance's ID with the job log type,
// follows the task's record to the blob, and streams the bundle from the
// blobstore. The bundle keeps its log files under their paths. The download
// runs for longer than bosh.request_timeout, which bounds only the wait for
// the director's answer to begin, and a bundle with no log files is an empty
// map and not a sentence.
func TestFetchLogsStreamsTheBundle(t *testing.T) {
	t.Parallel()

	t.Run("a slow body outlives the request timeout", func(t *testing.T) {
		t.Parallel()
		checkSlowBodyOutlivesTheRequestTimeout(t)
	})

	t.Run("a bundle with no log files", func(t *testing.T) {
		t.Parallel()

		server := newLogsDirector(t, logsConfig{bundle: serveBundle(t, logFile("./notes/readme.txt", "not a log"))})
		director := newTaskAdapter(t, server.Director)

		bundle := mustFetchLogs(t, director, nil)

		if bundle.Files == nil || len(bundle.Files) != 0 {
			t.Errorf("Files = %#v, want an empty map", bundle.Files)
		}
	})

	t.Run("a bundle that is not gzip", func(t *testing.T) {
		t.Parallel()

		server := newLogsDirector(t, logsConfig{bundle: func(writer http.ResponseWriter, _ *http.Request) {
			boshtest.WriteText(writer, http.StatusOK, "this is not a log bundle")
		}})
		director := newTaskAdapter(t, server.Director)

		_, err := fetchLogs(t, director, nil)
		if err == nil || !strings.Contains(err.Error(), "extract") {
			t.Fatalf("expected an error that names the extraction, got: %v", err)
		}
	})

	t.Run("a blob the director no longer holds", func(t *testing.T) {
		t.Parallel()

		server := newLogsDirector(t, logsConfig{bundle: func(writer http.ResponseWriter, _ *http.Request) {
			boshtest.WriteError(writer, http.StatusNotFound, 0, "blob is gone")
		}})
		director := newTaskAdapter(t, server.Director)

		_, err := fetchLogs(t, director, nil)

		var apiErr *boshapi.APIError
		if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
			t.Fatalf("expected the director's 404 to stay reachable, got %T: %v", err, err)
		}

		if !strings.Contains(err.Error(), logsBlob) {
			t.Errorf("expected the error to name the blob, got: %v", err)
		}
	})
}

// slowBundle streams a bundle one flushed entry at a time, so the whole
// download takes longer than a request timeout.
func slowBundle(t *testing.T) func(http.ResponseWriter, *http.Request) {
	t.Helper()

	return func(writer http.ResponseWriter, _ *http.Request) {
		bundle := newBundleWriter(t, writer, gzip.DefaultCompression)
		defer bundle.close()

		flusher, _ := writer.(http.Flusher)

		files := []bundleFile{
			logFile("./node/node.stdout.log", "first\n"),
			logFile("./node/node.stderr.log", "second\n"),
			logFile("./notes/readme.txt", "not a log"),
			logFile("./node/log/startup", "third\n"),
		}

		for index, file := range files {
			err := bundle.add(file)
			if err != nil {
				return
			}

			_ = bundle.tw.Flush()
			_ = bundle.gz.Flush()

			if flusher != nil {
				flusher.Flush()
			}

			if index < len(files)-1 {
				time.Sleep(150 * time.Millisecond)
			}
		}
	}
}

func checkSlowBodyOutlivesTheRequestTimeout(t *testing.T) {
	t.Helper()

	server := newLogsDirector(t, logsConfig{bundle: slowBundle(t)})
	director := newReadAdapter(t, server.Director, func(config *bosh.Config) {
		config.RequestTimeout = 250 * time.Millisecond
	})
	bosh.SetTaskPollInterval(director, testPollInterval)

	bundle := mustFetchLogs(t, director, nil)

	want := map[string]string{
		"node/node.stdout.log": "first\n",
		"node/node.stderr.log": "second\n",
		"node/log/startup":     "third\n",
	}

	if len(bundle.Files) != len(want) {
		t.Fatalf("Files = %v, want %v", bundle.Files, want)
	}

	for path, content := range want {
		if bundle.Files[path] != content {
			t.Errorf("Files[%q] = %q, want %q", path, bundle.Files[path], content)
		}
	}

	if len(bundle.Skipped) != 0 || bundle.StoppedEarly {
		t.Errorf("a whole bundle skipped %v and stopped early = %v", bundle.Skipped, bundle.StoppedEarly)
	}

	requests := server.APIRequests()
	if len(requests) == 0 || requests[0].Method != http.MethodGet || requests[0].Path != logsStartPath || requests[0].RawQuery != "type=job" {
		t.Fatalf("the first request was %+v, want GET %s?type=job", requests, logsStartPath)
	}

	if downloads := requestsTo(server.Director, http.MethodGet, logsBlobPath); len(downloads) != 1 {
		t.Errorf("expected one download of %s, got %d", logsBlobPath, len(downloads))
	}
}

// The blob ID is the result field of the task record. The task's result
// output holds something else and is never read, and a done task with an
// empty result has no bundle to fetch.
func TestFetchLogsReadsTheBlobFromTheTaskRecord(t *testing.T) {
	t.Parallel()

	t.Run("the record names the blob", func(t *testing.T) {
		t.Parallel()

		server := newLogsDirector(t, logsConfig{
			output: "not-the-blob",
			bundle: serveBundle(t, logFile("./node/node.log", "from the record\n")),
		})
		director := newTaskAdapter(t, server.Director)

		bundle := mustFetchLogs(t, director, nil)

		if bundle.Files["node/node.log"] != "from the record\n" {
			t.Errorf("Files = %v, want node/node.log from the blob in the record", bundle.Files)
		}

		if outputs := requestsTo(server.Director, http.MethodGet, detailsTaskPath+"/output"); len(outputs) != 0 {
			t.Errorf("the result output was read %d times, but the blob ID is in the task record", len(outputs))
		}

		if downloads := requestsTo(server.Director, http.MethodGet, "/resources/not-the-blob"); len(downloads) != 0 {
			t.Errorf("the output's text was downloaded as a blob %d times", len(downloads))
		}
	})

	t.Run("a done task with no result", func(t *testing.T) {
		t.Parallel()

		server := newLogsDirector(t, logsConfig{
			state:  func(int32, int32) (string, string) { return batchTaskDone, "" },
			output: "not-the-blob",
			bundle: serveBundle(t, logFile("./node/node.log", "never read\n")),
		})
		director := newTaskAdapter(t, server.Director)

		bundle, err := fetchLogs(t, director, nil)
		if err == nil {
			t.Fatalf("expected an error for a done task with no blob ID, got bundle %+v", bundle)
		}

		if !errors.Is(err, bosh.ErrNoLogBlob) {
			t.Errorf("expected the error to wrap ErrNoLogBlob, got: %v", err)
		}

		for _, want := range []string{"BOSH task 42", detailsDeployment, logsJob + "/" + logsInstance} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected the error to mention %q, got: %v", want, err)
			}
		}

		if downloads := requestsTo(server.Director, http.MethodGet, logsBlobPath); len(downloads) != 0 {
			t.Errorf("nothing should download without a blob ID, got %d downloads", len(downloads))
		}
	})
}

// A fetch task that ends in any state but done is a TaskFailedError that
// carries the director's text, and nothing downloads.
func TestFetchLogsFailsWhenTheTaskEndsInError(t *testing.T) {
	t.Parallel()

	for _, state := range []string{batchTaskError, taskCancelled, taskTimedOut} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()

			server := newLogsDirector(t, logsConfig{
				state:  func(int32, int32) (string, string) { return state, "the agent could not fetch logs" },
				bundle: serveBundle(t, logFile("./node/node.log", "never read\n")),
			})
			director := newTaskAdapter(t, server.Director)

			_, err := fetchLogs(t, director, nil)

			var failed *bosh.TaskFailedError
			if !errors.As(err, &failed) {
				t.Fatalf("expected a *bosh.TaskFailedError, got %T: %v", err, err)
			}

			if failed.Operation != logsOperation || failed.TaskID != detailsTaskID || failed.State != state ||
				failed.Result != "the agent could not fetch logs" || failed.Deployment != detailsDeployment {
				t.Errorf("unexpected TaskFailedError fields: %+v", failed)
			}

			if downloads := requestsTo(server.Director, http.MethodGet, logsBlobPath); len(downloads) != 0 {
				t.Errorf("a failed task has no bundle, but %d downloads were made", len(downloads))
			}
		})
	}
}

// The request's budget holds the bytes the whole request may keep, 64 MiB.
// A file that would overrun what remains is skipped and listed, a smaller
// file after it still fits, and what a file takes comes off the budget.
// With no budget a file keeps only its own 10 MiB limit.
func TestFetchLogsStopsAtTheTotalLimit(t *testing.T) {
	t.Parallel()

	if got := bosh.NewLogBudget().Remaining; got != 64*mebibyte {
		t.Fatalf("a new budget holds %d bytes, want 64 MiB", got)
	}

	t.Run("a shared budget", func(t *testing.T) {
		t.Parallel()

		files := make([]bundleFile, 0, 9)
		for index := range 8 {
			files = append(files, bigLogFile("./big/"+string(rune('a'+index))+".log", 9*mebibyte))
		}

		files = append(files, logFile("./small.log", "fits\n"))

		server := newLogsDirector(t, logsConfig{bundle: serveBundle(t, files...)})
		director := newTaskAdapter(t, server.Director)
		budget := bosh.NewLogBudget()

		bundle := mustFetchLogs(t, director, budget)

		// 7 files of 9 MiB take 63 MiB, so the eighth, which needs 9 MiB of
		// the 1 MiB left, is skipped and the small file after it fits.
		if len(bundle.Files) != 8 {
			t.Fatalf("kept %d files, want 7 big ones and small.log: %v", len(bundle.Files), keysOf(bundle.Files))
		}

		if _, kept := bundle.Files["big/h.log"]; kept {
			t.Error("big/h.log overran the budget and was kept")
		}

		if bundle.Files["small.log"] != "fits\n" {
			t.Errorf("small.log = %q, want it kept after the skip", bundle.Files["small.log"])
		}

		if len(bundle.Skipped) != 1 || bundle.Skipped[0] != "big/h.log" || bundle.SkippedCount != 1 {
			t.Errorf("Skipped = %v (%d), want only big/h.log", bundle.Skipped, bundle.SkippedCount)
		}

		// Each kept file is charged at what it takes as JSON.
		if want := 64*mebibyte - encodedCharge(t, bundle.Files); budget.Remaining != want {
			t.Errorf("the budget has %d bytes left, want %d", budget.Remaining, want)
		}

		if bundle.StoppedEarly {
			t.Error("a bundle read to its end was marked as stopped early")
		}
	})

	t.Run("no budget", func(t *testing.T) {
		t.Parallel()

		server := newLogsDirector(t, logsConfig{bundle: serveBundle(t, bigLogFile("./big.log", 12*mebibyte), logFile("./after.log", "after\n"))})
		director := newTaskAdapter(t, server.Director)

		bundle := mustFetchLogs(t, director, nil)

		if got := len(bundle.Files["big.log"]); got != 10*mebibyte {
			t.Errorf("big.log holds %d bytes, want the 10 MiB file limit", got)
		}

		if bundle.Files["after.log"] != "after\n" {
			t.Errorf("after.log = %q, want the entry after a cut file to be read", bundle.Files["after.log"])
		}
	})
}

// oversizedBundle streams a bundle with a few entries a log reader must
// refuse or cut, then padding that no log file keeps, then a log file past
// the padding. written counts the padding the director got to send, and
// ended closes once the handler returns.
func oversizedBundle(t *testing.T, longName string, padding int64, written *atomic.Int64, ended chan struct{}) func(http.ResponseWriter, *http.Request) {
	t.Helper()

	return func(writer http.ResponseWriter, _ *http.Request) {
		defer close(ended)

		bundle := newBundleWriter(t, writer, gzip.NoCompression)
		defer bundle.close()

		for _, file := range []bundleFile{
			logFile("./first.log", "kept\n"),
			{name: "./link.log", typeflag: tar.TypeSymlink, link: "/etc/passwd"},
			{name: "./pipe.log", typeflag: tar.TypeFifo},
			{name: "./device.log", typeflag: tar.TypeChar},
			{name: "./hard.log", typeflag: tar.TypeLink, link: "first.log"},
			logFile(longName, "long name\n"),
		} {
			err := bundle.add(file)
			if err != nil {
				return
			}
		}

		err := bundle.padding("./padding.bin", padding, written)
		if err != nil {
			return
		}

		_ = bundle.add(logFile("./after.log", "never reached\n"))
	}
}

// checkOversizedFiles checks which entries of the oversized bundle were
// kept.
func checkOversizedFiles(t *testing.T, bundle *bosh.LogBundle) {
	t.Helper()

	if _, reached := bundle.Files["after.log"]; reached {
		t.Error("after.log lies past the read limit and was kept")
	}

	if bundle.Files["first.log"] != "kept\n" {
		t.Errorf("first.log = %q, want it kept", bundle.Files["first.log"])
	}

	for _, name := range []string{"link.log", "pipe.log", "device.log", "hard.log"} {
		if _, kept := bundle.Files[name]; kept {
			t.Errorf("%s is not a regular file and was kept", name)
		}
	}

	var cut string

	for name := range bundle.Files {
		if strings.HasPrefix(name, "deep/nnnn") {
			cut = name
		}
	}

	if cut == "" {
		t.Fatalf("the 300-byte name is missing from %v", keysOf(bundle.Files))
	}

	if len(cut) != 256 || !strings.HasSuffix(cut, "[cut]") {
		t.Errorf("the 300-byte name became %d bytes %q, want 256 bytes marked as cut", len(cut), cut)
	}

	if bundle.Files[cut] != "long name\n" {
		t.Errorf("the cut name holds %q, want its content", bundle.Files[cut])
	}
}

// A bundle that decompresses past 512 MiB stops being read at that point,
// and the bundle says so. The download body closes at once, so the director
// is not made to send the rest. The bundle's links, devices, and FIFOs are
// skipped, and a name over 256 bytes is cut and marked.
func TestFetchLogsStopsOnAnOversizedBundle(t *testing.T) {
	t.Parallel()

	const padding = 600 * mebibyte

	var written atomic.Int64

	ended := make(chan struct{})

	longName := "./deep/" + strings.Repeat("n", 289) + ".log"
	if len(longName) != 300 {
		t.Fatalf("the test name is %d bytes, want 300", len(longName))
	}

	server := newLogsDirector(t, logsConfig{bundle: oversizedBundle(t, longName, padding, &written, ended)})
	director := newTaskAdapter(t, server.Director)

	bundle := mustFetchLogs(t, director, bosh.NewLogBudget())

	if !bundle.StoppedEarly {
		t.Error("a bundle past 512 MiB was not marked as stopped early")
	}

	checkOversizedFiles(t, bundle)

	// The body is closed once the limit is reached, so the director's write
	// of the padding fails with 88 MiB or more still to send.
	select {
	case <-ended:
	case <-time.After(30 * time.Second):
		t.Fatal("the director was still sending the bundle 30s after the fetch returned, so the body was not closed")
	}

	if sent := written.Load(); sent >= padding {
		t.Errorf("the director sent all %d bytes of padding, so the fetch read past its limit", sent)
	}
}

// A context that ends while the fetch task is queued cancels the task, and
// nothing downloads. The error names the task.
func TestFetchLogsCancelsTheTaskWhenTheContextEnds(t *testing.T) {
	t.Parallel()

	server := newLogsDirector(t, logsConfig{
		state:  func(_ int32, cancels int32) (string, string) { return queuedOrCancelled(cancels) },
		bundle: serveBundle(t, logFile("./node/node.log", "never read\n")),
	})
	director := newTaskAdapter(t, server.Director)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	go func() {
		<-server.firstPoll
		cancel()
	}()

	err := callWithin(t, 10*time.Second, func() error {
		_, err := director.FetchLogs(ctx, detailsDeployment, logsJob, logsInstance, nil)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected an error that wraps context.Canceled, got: %v", err)
	}

	_ = checkCallError(t, err, logsOperation, bosh.TaskCancelRequested)

	if deletes := requestsTo(server.Director, http.MethodDelete, detailsCancelPath); len(deletes) != 1 {
		t.Fatalf("expected exactly one DELETE %s, got %d", detailsCancelPath, len(deletes))
	}

	for _, want := range []string{cancelRequestedText, detailsTaskPage} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %v", want, err)
		}
	}

	if downloads := requestsTo(server.Director, http.MethodGet, logsBlobPath); len(downloads) != 0 {
		t.Errorf("a cancelled fetch downloaded %d times", len(downloads))
	}
}

func queuedOrCancelled(cancels int32) (string, string) {
	if cancels > 0 {
		return taskCancelled, "Task 42 cancelled"
	}

	return taskQueued, ""
}

// The caller's context also bounds the download. A body that stalls
// midway ends with the context, whose error the fetch reports.
func TestFetchLogsEndsWithTheContextDuringTheDownload(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	server := newLogsDirector(t, logsConfig{bundle: func(writer http.ResponseWriter, request *http.Request) {
		bundle := newBundleWriter(t, writer, gzip.DefaultCompression)
		defer bundle.close()

		err := bundle.add(logFile("./node/node.log", "begun\n"))
		if err != nil {
			return
		}

		_ = bundle.tw.Flush()
		_ = bundle.gz.Flush()

		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}

		select {
		case <-request.Context().Done():
		case <-release:
		}
	}})
	t.Cleanup(func() { close(release) })

	director := newTaskAdapter(t, server.Director)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	err := callWithin(t, 10*time.Second, func() error {
		_, err := director.FetchLogs(ctx, detailsDeployment, logsJob, logsInstance, nil)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected an error that wraps context.DeadlineExceeded, got: %v", err)
	}

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) || callErr.Operation != logsOperation {
		t.Errorf("expected a *bosh.DirectorCallError for %s, got %T: %v", logsOperation, err, err)
	}
}

// The director's deployment-not-found answer, code 70000, gives
// ErrDeploymentNotFound and keeps the director's answer. A lost answer is a
// DirectorCallError whose cause names the request, and the request is never
// resent.
func TestFetchLogsExplainsAFailedStart(t *testing.T) {
	t.Parallel()

	t.Run("a missing deployment", func(t *testing.T) {
		t.Parallel()

		server := newLogsDirector(t, logsConfig{start: func(writer http.ResponseWriter, _ *http.Request) {
			boshtest.WriteError(writer, http.StatusNotFound, boshtest.CodeDeploymentNotFound, "Deployment 'valkey-1' doesn't exist")
		}})
		director := newTaskAdapter(t, server.Director)

		_, err := fetchLogs(t, director, nil)
		if !errors.Is(err, bosh.ErrDeploymentNotFound) {
			t.Fatalf("expected ErrDeploymentNotFound, got: %v", err)
		}

		var apiErr *boshapi.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != boshtest.CodeDeploymentNotFound {
			t.Fatalf("expected the error to keep the director's answer with code 70000, got %T: %v", err, err)
		}

		if !strings.Contains(err.Error(), detailsDeployment) {
			t.Errorf("expected the error to name the deployment, got: %v", err)
		}

		if requests := server.APIRequests(); len(requests) != 1 || requests[0].Path != logsStartPath {
			t.Errorf("expected the start to be the only request, got %+v", requests)
		}
	})

	t.Run("a lost answer", func(t *testing.T) {
		t.Parallel()
		checkLostStartAnswer(t)
	})
}

func checkLostStartAnswer(t *testing.T) {
	t.Helper()

	server := newLogsDirector(t, logsConfig{start: func(writer http.ResponseWriter, _ *http.Request) {
		boshtest.DropAnswer(writer)
	}})
	director := newTaskAdapter(t, server.Director)

	_, err := fetchLogs(t, director, nil)
	if err == nil {
		t.Fatal("expected an error for a lost answer")
	}

	if starts := requestsTo(server.Director, http.MethodGet, logsStartPath); len(starts) != 1 {
		t.Fatalf("expected exactly one GET %s, got %d", logsStartPath, len(starts))
	}

	method, path, lost := bosh.AnswerLostRequest(err)
	if !lost {
		t.Fatalf("expected a DirectorCallError whose cause is a lost answer, got %T: %v", err, err)
	}

	// The cause names the request as boshapi sends it, query and all.
	if method != http.MethodGet || path != logsStartPath+"?type=job" {
		t.Errorf("expected the cause to name GET %s?type=job, got %s %s", logsStartPath, method, path)
	}

	var callErr *bosh.DirectorCallError
	if !errors.As(err, &callErr) || callErr.Operation != logsOperation {
		t.Errorf("expected the call error to name %s, got %T: %v", logsOperation, err, err)
	}

	if errors.Is(err, bosh.ErrOutcomeUnknown) || boshapi.IsOutcomeUnknown(err) {
		t.Errorf("a lost answer to a read must not read as an unknown outcome: %v", err)
	}

	for _, want := range []string{"the caller can ask again", "read-only task", "did not resend"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected the error to mention %q, got: %v", want, err)
		}
	}
}

func keysOf(files map[string]string) []string {
	keys := make([]string, 0, len(files))
	for key := range files {
		keys = append(keys, key)
	}

	return keys
}

// bundleBytes builds a gzipped tar of files in memory.
func bundleBytes(t *testing.T, files ...bundleFile) *bytes.Buffer {
	t.Helper()

	var buffer bytes.Buffer

	bundle := newBundleWriter(t, &buffer, gzip.BestSpeed)

	for _, file := range files {
		err := bundle.add(file)
		if err != nil {
			t.Fatalf("writing %s into the test bundle: %v", file.name, err)
		}
	}

	bundle.close()

	return &buffer
}

// pipedBundle streams the bundle that write builds through a pipe, so a
// bundle of hundreds of MiB never sits in memory. With trailer false the tar
// ends after its last entry, with no end-of-archive blocks. The pipe closes
// when the test ends, which stops a writer the reader left behind.
func pipedBundle(t *testing.T, trailer bool, write func(bundle *bundleWriter) error) io.Reader {
	t.Helper()

	reader, writer := io.Pipe()
	bundle := newBundleWriter(t, writer, gzip.NoCompression)
	done := make(chan struct{})

	go func() {
		defer close(done)

		err := write(bundle)
		if err == nil && trailer {
			err = bundle.tw.Close()
		} else if err == nil {
			err = bundle.tw.Flush()
		}

		if err == nil {
			err = bundle.gz.Close()
		}

		_ = writer.CloseWithError(err)
	}()

	t.Cleanup(func() {
		_ = reader.Close()

		<-done
	})

	return reader
}

func mustExtract(t *testing.T, data io.Reader, budget *bosh.LogBudget) *bosh.LogBundle {
	t.Helper()

	bundle, err := bosh.ExtractLogsFromTarGz(data, budget)
	if err != nil {
		t.Fatalf("expected the bundle to be read, got: %v", err)
	}

	if bundle == nil {
		t.Fatal("the read returned no bundle and no error")
	}

	return bundle
}

// A log file whose content runs past the read limit is skipped and listed,
// the bundle is marked as stopped early, and the files read before it are
// kept and charged.
func TestFetchLogsSkipsAFileThatPassesTheReadLimit(t *testing.T) {
	t.Parallel()

	// first.log takes 1 KiB and the padding header 512 bytes, so 511 MiB of
	// padding puts the 2 MiB of straddle.log across the 512 MiB limit.
	data := pipedBundle(t, true, func(bundle *bundleWriter) error {
		err := bundle.add(logFile("./first.log", keptContent))
		if err != nil {
			return err
		}

		err = bundle.padding("./padding.bin", 511*mebibyte, nil)
		if err != nil {
			return err
		}

		err = bundle.add(bigLogFile("./straddle.log", 2*mebibyte))
		if err != nil {
			return err
		}

		return bundle.add(logFile("./after.log", "never reached\n"))
	})

	budget := bosh.NewLogBudget()
	bundle := mustExtract(t, data, budget)

	if !bundle.StoppedEarly {
		t.Error("a bundle whose file passed the read limit was not marked as stopped early")
	}

	if len(bundle.Files) != 1 || bundle.Files["first.log"] != keptContent {
		t.Errorf("Files = %v, want only first.log", keysOf(bundle.Files))
	}

	if bundle.SkippedCount != 1 || len(bundle.Skipped) != 1 || bundle.Skipped[0] != "straddle.log" {
		t.Errorf("Skipped = %v (%d), want only straddle.log", bundle.Skipped, bundle.SkippedCount)
	}

	if charged := 64*mebibyte - budget.Remaining; charged != encodedCharge(t, bundle.Files) {
		t.Errorf("the budget was charged %d bytes, want %d for first.log alone", charged, encodedCharge(t, bundle.Files))
	}
}

// A bundle of exactly the read limit reads to its end, so it is not marked
// as stopped early, even when its tar has no end-of-archive blocks and the
// reader must look past the limit to find the end.
func TestFetchLogsReadsABundleOfExactlyTheReadLimit(t *testing.T) {
	t.Parallel()

	// first.log takes 1 KiB and the padding header 512 bytes, so the
	// padding brings the tar to exactly 512 MiB.
	data := pipedBundle(t, false, func(bundle *bundleWriter) error {
		err := bundle.add(logFile("./first.log", keptContent))
		if err != nil {
			return err
		}

		return bundle.padding("./padding.bin", 512*mebibyte-1536, nil)
	})

	bundle := mustExtract(t, data, nil)

	if bundle.StoppedEarly {
		t.Error("a bundle of exactly 512 MiB was marked as stopped early")
	}

	if bundle.Files["first.log"] != keptContent || bundle.SkippedCount != 0 {
		t.Errorf("Files = %v and %d skipped, want first.log and nothing skipped", keysOf(bundle.Files), bundle.SkippedCount)
	}
}

// A bundle keeps at most 5000 files. The file past that is skipped and
// listed.
func TestFetchLogsKeepsAtMostFiveThousandFiles(t *testing.T) {
	t.Parallel()

	files := make([]bundleFile, 0, 5001)
	for index := range 5001 {
		files = append(files, logFile(fmt.Sprintf("./many/%04d.log", index), "x"))
	}

	bundle := mustExtract(t, bundleBytes(t, files...), nil)

	if len(bundle.Files) != 5000 {
		t.Errorf("kept %d files, want 5000", len(bundle.Files))
	}

	if bundle.SkippedCount != 1 || len(bundle.Skipped) != 1 || bundle.Skipped[0] != "many/5000.log" {
		t.Errorf("Skipped = %v (%d), want only many/5000.log", bundle.Skipped, bundle.SkippedCount)
	}
}

// Two entries whose names clean to the same path would overwrite each
// other in the map, so the second is skipped and listed.
func TestFetchLogsSkipsARepeatedName(t *testing.T) {
	t.Parallel()

	bundle := mustExtract(t, bundleBytes(t, logFile("./node/a.log", firstContent), logFile("node//a.log", "second\n")), nil)

	if len(bundle.Files) != 1 || bundle.Files["node/a.log"] != firstContent {
		t.Errorf("Files = %v, want node/a.log with the first entry's content", bundle.Files)
	}

	if bundle.SkippedCount != 1 || len(bundle.Skipped) != 1 || bundle.Skipped[0] != "node/a.log" {
		t.Errorf("Skipped = %v (%d), want the repeated node/a.log", bundle.Skipped, bundle.SkippedCount)
	}
}

// Names that are not UTF-8 are made valid before they are kept, so two names
// that JSON would write the same are caught as a repeat, and the response
// never holds a duplicate key.
func TestFetchLogsMakesNamesValidUTF8(t *testing.T) {
	t.Parallel()

	bundle := mustExtract(t, bundleBytes(t, logFile("./a\xff.log", firstContent), logFile("./a\xfe.log", "second\n")), nil)

	const valid = "a�.log"

	if len(bundle.Files) != 1 || bundle.Files[valid] != firstContent {
		t.Errorf("Files = %q, want only %q with the first entry's content", keysOf(bundle.Files), valid)
	}

	if bundle.SkippedCount != 1 || len(bundle.Skipped) != 1 || bundle.Skipped[0] != valid {
		t.Errorf("Skipped = %q (%d), want the second name as %q", bundle.Skipped, bundle.SkippedCount, valid)
	}

	encoded, err := json.Marshal(bundle.Files)
	if err != nil {
		t.Fatalf("encoding the files: %v", err)
	}

	var decoded map[string]string

	err = json.Unmarshal(encoded, &decoded)
	if err != nil || len(decoded) != len(bundle.Files) {
		t.Errorf("the files encode as %s, which decodes to %d keys for %d files: %v", encoded, len(decoded), len(bundle.Files), err)
	}
}

// A bundle lists at most 1000 skipped paths, and its count stays exact.
func TestFetchLogsListsAtMostAThousandSkippedPaths(t *testing.T) {
	t.Parallel()

	files := make([]bundleFile, 0, 1002)
	for range 1002 {
		files = append(files, logFile("./node/a.log", "again\n"))
	}

	bundle := mustExtract(t, bundleBytes(t, files...), nil)

	if bundle.SkippedCount != 1001 {
		t.Errorf("SkippedCount = %d, want 1001", bundle.SkippedCount)
	}

	if len(bundle.Skipped) != 1000 {
		t.Errorf("Skipped lists %d paths, want 1000", len(bundle.Skipped))
	}
}

// A long name made of two-byte characters is cut on a character boundary,
// so the cut name is still UTF-8.
func TestFetchLogsCutsANameOnACharacterBoundary(t *testing.T) {
	t.Parallel()

	// "deep/" is 5 bytes, so byte 248, where a plain cut would end, falls
	// inside a character.
	name := "./deep/" + strings.Repeat("é", 150) + ".log"

	bundle := mustExtract(t, bundleBytes(t, logFile(name, "long name\n")), nil)

	if len(bundle.Files) != 1 {
		t.Fatalf("Files = %q, want the one file", keysOf(bundle.Files))
	}

	for cut, content := range bundle.Files {
		if !utf8.ValidString(cut) {
			t.Errorf("the cut name %q is not valid UTF-8", cut)
		}

		if len(cut) != 255 || !strings.HasPrefix(cut, "deep/é") || !strings.HasSuffix(cut, "...[cut]") {
			t.Errorf("the name became %d bytes %q, want 255 bytes marked as cut", len(cut), cut)
		}

		if content != "long name\n" {
			t.Errorf("the cut name holds %q, want its content", content)
		}
	}
}

// A file past the 10 MiB file limit is weighed against the budget at the
// 10 MiB it keeps, not at its whole size, so a 12 MiB file fits 11 MiB.
func TestFetchLogsWeighsAFileAtTheSizeItKeeps(t *testing.T) {
	t.Parallel()

	budget := &bosh.LogBudget{Remaining: 11 * mebibyte}

	bundle := mustExtract(t, bundleBytes(t, bigLogFile("./big.log", 12*mebibyte)), budget)

	if got := len(bundle.Files["big.log"]); got != 10*mebibyte {
		t.Errorf("big.log holds %d bytes, want the 10 MiB it keeps", got)
	}

	if bundle.SkippedCount != 0 {
		t.Errorf("Skipped = %v, want nothing skipped", bundle.Skipped)
	}
}

// A file past the 10 MiB file limit keeps its first 10 MiB, and the bundle
// lists it as cut, so a reader knows the file holds more.
func TestFetchLogsRecordsTheFilesItCut(t *testing.T) {
	t.Parallel()

	bundle := mustExtract(t, bundleBytes(t,
		bigLogFile("./big.log", 12*mebibyte),
		bigLogFile("./whole.log", 10*mebibyte),
		logFile("./small.log", "small\n"),
	), nil)

	if got := len(bundle.Files["big.log"]); got != 10*mebibyte {
		t.Errorf("big.log holds %d bytes, want its first 10 MiB", got)
	}

	if len(bundle.Cut) != 1 || bundle.Cut[0] != "big.log" || bundle.CutCount != 1 {
		t.Errorf("Cut = %v (%d), want only big.log", bundle.Cut, bundle.CutCount)
	}

	if bundle.SkippedCount != 0 {
		t.Errorf("Skipped = %v, want nothing skipped", bundle.Skipped)
	}
}

// A bundle that fails partway returns no files, so it charges nothing to
// the request's budget, and later instances keep the whole of it.
func TestFetchLogsChargesNothingForABundleThatFails(t *testing.T) {
	t.Parallel()

	server := newLogsDirector(t, logsConfig{bundle: func(writer http.ResponseWriter, _ *http.Request) {
		bundle := newBundleWriter(t, writer, gzip.NoCompression)

		err := bundle.add(bigLogFile("./a.log", 9*mebibyte))
		if err != nil {
			return
		}

		err = bundle.add(bigLogFile("./b.log", 9*mebibyte))
		if err != nil {
			return
		}

		// The stream ends partway through the gzip data, with no tar end and
		// no gzip trailer.
		_ = bundle.tw.Flush()
		_ = bundle.gz.Flush()
	}})
	director := newTaskAdapter(t, server.Director)
	budget := bosh.NewLogBudget()

	bundle, err := fetchLogs(t, director, budget)
	if err == nil {
		t.Fatalf("expected a cut-off bundle to fail, got %d files", len(bundle.Files))
	}

	if budget.Remaining != 64*mebibyte {
		t.Errorf("a failed bundle charged %d bytes to the budget, want none", 64*mebibyte-budget.Remaining)
	}
}

// encodedCharge is what files take as JSON: the length of the encoded map
// less its closing brace, since the charge for each file counts the
// separator after it.
func encodedCharge(t *testing.T, files map[string]string) int64 {
	t.Helper()

	encoded, err := json.Marshal(files)
	if err != nil {
		t.Fatalf("encoding the files: %v", err)
	}

	return int64(len(encoded)) - 1
}

// The budget is charged with what each file takes once it is encoded as
// JSON, which is up to six times its size, so the 64 MiB budget bounds the
// response as it is written. Two 10 MiB files of escape and markup bytes
// take 60 MiB each, so the second is skipped.
func TestFetchLogsChargesTheEncodedSize(t *testing.T) {
	t.Parallel()

	escaped := strings.Repeat("\x1b<", 5*mebibyte)
	mixed := "line\n\ttab \"quoted\" back\\slash é   \xff\xfe & > \x00 \x7f plain\n"

	budget := bosh.NewLogBudget()
	bundle := mustExtract(t, bundleBytes(t,
		logFile("./escaped.log", escaped),
		logFile("./mixed\x01.log", mixed),
		logFile("./again.log", escaped),
	), budget)

	if bundle.Files["escaped.log"] != escaped || len(bundle.Files) != 2 {
		t.Fatalf("Files = %q, want escaped.log and the mixed file", keysOf(bundle.Files))
	}

	if bundle.SkippedCount != 1 || bundle.Skipped[0] != "again.log" {
		t.Errorf("Skipped = %v (%d), want again.log, which would take 60 MiB of the 4 MiB left", bundle.Skipped, bundle.SkippedCount)
	}

	charged := 64*mebibyte - budget.Remaining
	want := encodedCharge(t, bundle.Files)

	if diff := charged - want; diff < -8 || diff > 8 {
		t.Errorf("the budget was charged %d bytes, want the %d bytes the files take as JSON", charged, want)
	}
}

// The task record's result may carry the space and newline the director
// writes around it, and the blob is still found.
func TestFetchLogsTrimsTheBlobID(t *testing.T) {
	t.Parallel()

	server := newLogsDirector(t, logsConfig{
		state:  func(int32, int32) (string, string) { return batchTaskDone, "  " + logsBlob + "\n" },
		bundle: serveBundle(t, logFile("./node/node.log", "trimmed\n")),
	})
	director := newTaskAdapter(t, server.Director)

	bundle := mustFetchLogs(t, director, nil)

	if bundle.Files["node/node.log"] != "trimmed\n" {
		t.Errorf("Files = %v, want node/node.log from %s", bundle.Files, logsBlob)
	}
}

// nilLogsClient starts a log fetch with no task and no error, which a
// well-behaved boshapi never does.
type nilLogsClient struct {
	boshapi.Client
}

func (nilLogsClient) Instances() boshapi.InstancesClient { return nilLogsInstances{} }

type nilLogsInstances struct {
	boshapi.InstancesClient
}

func (nilLogsInstances) FetchLogs(context.Context, string, string, string, *boshapi.LogOptions) (*boshapi.Task, error) {
	return nil, nil //nolint:nilnil // the answer under test
}

// A start that returns no task and no error is an error that names the
// director, not a nil dereference.
func TestFetchLogsExplainsAStartWithNoTask(t *testing.T) {
	t.Parallel()

	server := newLogsDirector(t, logsConfig{})
	director := newTaskAdapter(t, server.Director)

	bosh.WithAPI(director, nilLogsClient{})

	_, err := fetchLogs(t, director, nil)
	if !errors.Is(err, bosh.ErrEmptyDirectorAnswer) {
		t.Fatalf("expected ErrEmptyDirectorAnswer, got: %v", err)
	}

	if !strings.Contains(err.Error(), server.URL) {
		t.Errorf("expected the error to name the director, got: %v", err)
	}
}

// hangUntil holds a request open until release closes or the client goes
// away, as a director that never answers does.
func hangUntil(release <-chan struct{}) func(http.ResponseWriter, *http.Request) {
	return func(_ http.ResponseWriter, request *http.Request) {
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}
}

// checkCancelBound runs call under a context with a deadline of one second,
// against a director whose task stays queued and whose cancel hangs, and
// checks that call returns within the deadline, the 3 second bound on the
// cancel, and one second of slack for the poll and a loaded test run. The
// error says the cancel failed.
func checkCancelBound(t *testing.T, operation string, call func(ctx context.Context) error) {
	t.Helper()

	const (
		deadline    = 1 * time.Second
		cancelBound = 3 * time.Second
		slack       = 1 * time.Second
	)

	ctx, cancel := context.WithTimeout(t.Context(), deadline)
	defer cancel()

	started := time.Now()
	err := callWithin(t, 30*time.Second, func() error { return call(ctx) })
	elapsed := time.Since(started)

	if limit := deadline + cancelBound + slack; elapsed > limit {
		t.Errorf("the call returned %v after it started, want no more than %v: its deadline, the cancel bound, and slack", elapsed, limit)
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected an error that wraps context.DeadlineExceeded, got: %v", err)
	}

	_ = checkCallError(t, err, operation, bosh.TaskCancelFailed)
}

// A read-only task that runs under a request's deadline is cancelled once
// the deadline passes, and the cancel is bounded to 3 seconds, so a
// director that never answers the cancel cannot hold the request through
// the margin it keeps to write its response.
func TestFetchLogsBoundsTheCancelAfterTheDeadline(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	server := newLogsDirector(t, logsConfig{
		state:  func(int32, int32) (string, string) { return taskQueued, "" },
		cancel: hangUntil(release),
	})
	t.Cleanup(func() { close(release) })

	director := newTaskAdapter(t, server.Director)

	checkCancelBound(t, logsOperation, func(ctx context.Context) error {
		_, err := director.FetchLogs(ctx, detailsDeployment, logsJob, logsInstance, nil)

		return err //nolint:wrapcheck // the test asserts on the adapter's own error
	})

	if deletes := requestsTo(server.Director, http.MethodDelete, detailsCancelPath); len(deletes) != 1 {
		t.Errorf("expected exactly one DELETE %s, got %d", detailsCancelPath, len(deletes))
	}
}

// The VM listing the logs endpoint makes runs under the same deadline, so
// its cancel has the same bound.
func TestGetDeploymentVMsBoundsTheCancelAfterTheDeadline(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	server := newDetailsDirector(t, detailsConfig{
		state: func(int32, int32) (string, string) { return taskQueued, "" },
		cancel: func(http.ResponseWriter) {
			<-release
		},
	})
	t.Cleanup(func() { close(release) })

	director := newTaskAdapter(t, server.Director)

	checkCancelBound(t, detailsVMListing, func(ctx context.Context) error {
		return vmListing().call(ctx, director)
	})
}
