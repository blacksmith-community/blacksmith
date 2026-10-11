package boshtest

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Task states the SSH director uses.
const (
	TaskQueued     = "queued"
	TaskProcessing = "processing"
	TaskCancelling = "cancelling"
	TaskDone       = "done"
	TaskError      = "error"
	TaskCancelled  = "cancelled"
	TaskTimeout    = "timeout"
)

// SSH commands the director's SSH route runs.
const (
	SSHSetup   = "setup"
	SSHCleanup = "cleanup"
)

// contextIDHeader carries the context ID a request was sent with.
const contextIDHeader = "X-Bosh-Context-Id"

// verboseAll is the task list verbosity at which the director lists SSH
// tasks.
const verboseAll = "2"

// internalErrorCode is the director's error code for an internal error.
const internalErrorCode = 100

// firstSSHTaskID is the ID of the first task the SSH director starts.
const firstSSHTaskID = 100

// SSHCall is one request to a deployment's SSH route.
type SSHCall struct {
	// Deployment is the deployment the path names.
	Deployment string
	// Command is the command the body names.
	Command string
	// Body is the request body as sent.
	Body string
	// Fields is the body decoded as JSON, or nil when it does not decode.
	Fields map[string]interface{}
	// ContextID is the X-Bosh-Context-Id the request carried.
	ContextID string
	// TaskID is the task the request started, or 0 when it started none.
	TaskID int
	// At is when the request arrived.
	At time.Time
}

// User returns the user a setup names, or the user_regex a cleanup names.
func (c SSHCall) User() string {
	params, _ := c.Fields["params"].(map[string]interface{})

	if user, ok := params["user"].(string); ok {
		return user
	}

	regex, _ := params["user_regex"].(string)

	return regex
}

// SSHAnswer says how the SSH director answers one SSH request.
type SSHAnswer struct {
	// Status, when not zero, answers with the director's JSON error body
	// for Code and Description, and starts no task.
	Status      int
	Code        int
	Description string
	// Drop starts the task and closes the connection without an answer.
	Drop bool
	// State is the state the task starts in. Empty means done.
	State string
	// Result is the task's result text, which the director fills in when
	// a task fails.
	Result string
	// Output is the task's result output. Empty means one success entry
	// for 127.0.0.1 for a setup, and nothing for a cleanup.
	Output string
	// FailReads is how many reads of the task, from the first, answer with
	// the director's internal error in place of the task.
	FailReads int
	// OnRead, when set, runs as each read of the task arrives, before the
	// read is answered, so a test can act once the client is waiting on
	// the task.
	OnRead func()
	// OutputStatus, when not zero, answers each read of the task's result
	// output with the director's JSON error body for that status.
	OutputStatus int
	// HoldOutput, when set, runs before each read of the task's result
	// output is answered, with the read's request context.
	HoldOutput func(context.Context)
	// Hold, when set, runs before the SSH request itself is answered,
	// with the request's context, so a test can hold the answer and see
	// when the client gives up on it. The request is recorded and its
	// task started only once Hold returns.
	Hold func(context.Context)
}

// SSHTask is a task the SSH director started, or one a test listed.
type SSHTask struct {
	ID          int
	Deployment  string
	Command     string
	Description string
	State       string
	Result      string
	Output      string
	ContextID   string
	// EndedAt is when the task reached a terminal state, zero while it
	// runs.
	EndedAt time.Time

	failReads    int
	onRead       func()
	outputStatus int
	holdOutput   func(context.Context)
}

// SSHCancel is one cancel the SSH director received.
type SSHCancel struct {
	TaskID int
	// State is the task's state when the cancel arrived.
	State string
	At    time.Time
}

// SSHDirector is a test director that serves a deployment's SSH route and
// the tasks it starts. A setup or cleanup task starts in the state the
// answer gives and stays there until the test moves it. A cancel ends a
// queued task as cancelled at once, marks a processing task as cancelling,
// which a started SSH job ignores, and is refused for a task that has
// ended, all as the director does. Its task list, like the director's,
// names an SSH task only at verbose 2.
type SSHDirector struct {
	*Director

	// HostKey is the host key the default setup answer reports for its
	// instance. A test VM that presents it passes the adapter's pin.
	HostKey ssh.Signer

	answer func(SSHCall) SSHAnswer

	mu       sync.Mutex
	nextID   int
	calls    []SSHCall
	tasks    map[int]*SSHTask
	cancels  []SSHCancel
	reads    map[int]int
	listings []url.Values
}

// NewSSH starts an SSH director. answer decides how each SSH request is
// answered, and may block to hold the request. A nil answer starts every
// task done. Requests the SSH director does not serve go to the handler.
func NewSSH(tb testing.TB, answer func(SSHCall) SSHAnswer, handler Handler) *SSHDirector {
	tb.Helper()

	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatalf("boshtest: generate the SSH host key: %v", err)
	}

	hostKey, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		tb.Fatalf("boshtest: make the SSH host signer: %v", err)
	}

	director := &SSHDirector{
		HostKey: hostKey,
		answer:  answer,
		nextID:  firstSSHTaskID,
		tasks:   make(map[int]*SSHTask),
		reads:   make(map[int]int),
	}

	director.Director = New(tb, func(writer http.ResponseWriter, request *http.Request) bool {
		if director.serve(writer, request) {
			return true
		}

		return handler != nil && handler(writer, request)
	})

	return director
}

// AddTask lists a task the SSH director did not start, such as a setup
// another request started, and returns its ID.
func (d *SSHDirector) AddTask(deployment, command, state string) int {
	return d.start(&SSHTask{
		Deployment:  deployment,
		Command:     command,
		Description: "ssh: " + command + ":{}",
		State:       state,
	})
}

// SetState moves a task to state, and records when it ended.
func (d *SSHDirector) SetState(taskID int, state string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	task, found := d.tasks[taskID]
	if !found {
		panic(fmt.Sprintf("boshtest: no task %d to set to %s", taskID, state))
	}

	task.State = state

	if isTerminal(state) && task.EndedAt.IsZero() {
		task.EndedAt = time.Now()
	}
}

// SetResult sets a task's result text.
func (d *SSHDirector) SetResult(taskID int, result string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	task, found := d.tasks[taskID]
	if !found {
		panic(fmt.Sprintf("boshtest: no task %d to set the result of", taskID))
	}

	task.Result = result
}

// Task returns a copy of the task with the ID.
func (d *SSHDirector) Task(taskID int) (SSHTask, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	task, found := d.tasks[taskID]
	if !found {
		return SSHTask{}, false
	}

	return *task, true
}

// Reads reports how many times the task was read.
func (d *SSHDirector) Reads(taskID int) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.reads[taskID]
}

// Calls returns every SSH request so far, oldest first.
func (d *SSHDirector) Calls() []SSHCall {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]SSHCall(nil), d.calls...)
}

// CallsOf returns the SSH requests that named the command, oldest first.
func (d *SSHDirector) CallsOf(command string) []SSHCall {
	var found []SSHCall

	for _, call := range d.Calls() {
		if call.Command == command {
			found = append(found, call)
		}
	}

	return found
}

// Listings returns the query of every task list request so far, oldest
// first.
func (d *SSHDirector) Listings() []url.Values {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]url.Values(nil), d.listings...)
}

// Cancels returns every cancel so far, oldest first.
func (d *SSHDirector) Cancels() []SSHCancel {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]SSHCancel(nil), d.cancels...)
}

func (d *SSHDirector) serve(writer http.ResponseWriter, request *http.Request) bool {
	path := request.URL.Path

	switch {
	case request.Method == http.MethodPost && strings.HasPrefix(path, "/deployments/") && strings.HasSuffix(path, "/ssh"):
		d.serveSSH(writer, request, strings.TrimSuffix(strings.TrimPrefix(path, "/deployments/"), "/ssh"))
	case request.Method == http.MethodGet && path == "/tasks":
		d.serveTaskList(writer, request)
	case request.Method == http.MethodGet && strings.HasPrefix(path, "/tasks/") && strings.HasSuffix(path, "/output"):
		return d.serveOutput(writer, request, strings.TrimSuffix(strings.TrimPrefix(path, "/tasks/"), "/output"))
	case request.Method == http.MethodGet && strings.HasPrefix(path, "/tasks/"):
		return d.serveTask(writer, strings.TrimPrefix(path, "/tasks/"))
	case request.Method == http.MethodDelete && strings.HasPrefix(path, "/task/"):
		return d.serveCancel(writer, strings.TrimPrefix(path, "/task/"))
	default:
		return false
	}

	return true
}

func (d *SSHDirector) serveSSH(writer http.ResponseWriter, request *http.Request, deployment string) {
	body, _ := io.ReadAll(request.Body)

	call := SSHCall{
		Deployment: deployment,
		Body:       string(body),
		ContextID:  request.Header.Get(contextIDHeader),
		At:         time.Now(),
	}

	if json.Unmarshal(body, &call.Fields) == nil {
		call.Command, _ = call.Fields["command"].(string)
	}

	var answer SSHAnswer
	if d.answer != nil {
		answer = d.answer(call)
	}

	if answer.Hold != nil {
		answer.Hold(request.Context())
	}

	if answer.Status != 0 {
		d.record(call)
		WriteError(writer, answer.Status, answer.Code, answer.Description)

		return
	}

	task := &SSHTask{
		Deployment:  deployment,
		Command:     call.Command,
		Description: "ssh: " + call.Command + ":" + targetText(call.Fields),
		State:       answer.State,
		Result:      answer.Result,
		Output:      answer.Output,
		ContextID:   call.ContextID,

		failReads:    answer.FailReads,
		onRead:       answer.OnRead,
		outputStatus: answer.OutputStatus,
		holdOutput:   answer.HoldOutput,
	}

	if task.State == "" {
		task.State = TaskDone
	}

	if task.Output == "" && call.Command == SSHSetup {
		task.Output = defaultSetupOutput(call.Fields, AuthorizedKey(d.HostKey.PublicKey()))
	}

	call.TaskID = d.start(task)
	d.record(call)

	if answer.Drop {
		DropAnswer(writer)

		return
	}

	writer.Header().Set("Location", "/tasks/"+strconv.Itoa(call.TaskID))
	writer.WriteHeader(http.StatusFound)
}

func (d *SSHDirector) record(call SSHCall) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls = append(d.calls, call)
}

func (d *SSHDirector) start(task *SSHTask) int {
	d.mu.Lock()
	defer d.mu.Unlock()

	task.ID = d.nextID
	d.nextID++

	if isTerminal(task.State) {
		task.EndedAt = time.Now()
	}

	d.tasks[task.ID] = task

	return task.ID
}

func (d *SSHDirector) serveTaskList(writer http.ResponseWriter, request *http.Request) {
	query := request.URL.Query()
	deployment := query.Get("deployment")
	contextID := query.Get("context_id")
	// The director lists SSH tasks only at verbose 2. Its default
	// verbosity of 1 lists a fixed set of task types, and ssh is not one.
	listsSSH := query.Get("verbose") == verboseAll

	var states []string
	if state := query.Get("state"); state != "" {
		states = strings.Split(state, ",")
	}

	d.mu.Lock()

	d.listings = append(d.listings, query)

	ids := make([]int, 0, len(d.tasks))
	for id := range d.tasks {
		ids = append(ids, id)
	}

	// The director lists the newest task first.
	slices.Sort(ids)
	slices.Reverse(ids)

	listed := make([]map[string]interface{}, 0, len(ids))

	for _, id := range ids {
		task := d.tasks[id]

		if deployment != "" && task.Deployment != deployment {
			continue
		}

		if contextID != "" && task.ContextID != contextID {
			continue
		}

		if len(states) > 0 && !slices.Contains(states, task.State) {
			continue
		}

		if !listsSSH {
			continue
		}

		listed = append(listed, taskJSON(task))
	}

	d.mu.Unlock()

	WriteJSON(writer, http.StatusOK, listed)
}

func (d *SSHDirector) serveTask(writer http.ResponseWriter, idText string) bool {
	d.mu.Lock()

	task, found := d.lookup(idText)
	if !found {
		d.mu.Unlock()

		return false
	}

	d.reads[task.ID]++
	answer := taskJSON(task)
	onRead := task.onRead

	failed := task.failReads > 0
	if failed {
		task.failReads--
	}

	d.mu.Unlock()

	if onRead != nil {
		onRead()
	}

	if failed {
		WriteError(writer, http.StatusInternalServerError, internalErrorCode, "The database is locked.")

		return true
	}

	WriteJSON(writer, http.StatusOK, answer)

	return true
}

func (d *SSHDirector) serveOutput(writer http.ResponseWriter, request *http.Request, idText string) bool {
	d.mu.Lock()

	task, found := d.lookup(idText)
	if !found {
		d.mu.Unlock()

		return false
	}

	output, status, hold := task.Output, task.outputStatus, task.holdOutput

	d.mu.Unlock()

	if hold != nil {
		hold(request.Context())
	}

	if status != 0 {
		WriteError(writer, status, internalErrorCode, "The task's result could not be read.")

		return true
	}

	WriteText(writer, http.StatusOK, output)

	return true
}

func (d *SSHDirector) serveCancel(writer http.ResponseWriter, idText string) bool {
	d.mu.Lock()

	task, found := d.lookup(idText)
	if !found {
		d.mu.Unlock()

		return false
	}

	d.cancels = append(d.cancels, SSHCancel{TaskID: task.ID, State: task.State, At: time.Now()})

	switch task.State {
	case TaskQueued:
		task.State = TaskCancelled
		task.EndedAt = time.Now()
	case TaskProcessing:
		task.State = TaskCancelling
	default:
		state := task.State

		d.mu.Unlock()

		WriteText(writer, http.StatusBadRequest, fmt.Sprintf("Cannot cancel task %d: invalid state (%s)", task.ID, state))

		return true
	}

	d.mu.Unlock()

	writer.WriteHeader(http.StatusNoContent)

	return true
}

// lookup finds a task by the ID in a path. The caller holds d.mu.
func (d *SSHDirector) lookup(idText string) (*SSHTask, bool) {
	id, err := strconv.Atoi(idText)
	if err != nil {
		return nil, false
	}

	task, found := d.tasks[id]

	return task, found
}

func taskJSON(task *SSHTask) map[string]interface{} {
	return map[string]interface{}{
		"id":          task.ID,
		"state":       task.State,
		"description": task.Description,
		"timestamp":   time.Now().Unix(),
		"started_at":  time.Now().Unix(),
		"result":      task.Result,
		"user":        "admin",
		"deployment":  task.Deployment,
		"context_id":  task.ContextID,
	}
}

func isTerminal(state string) bool {
	switch state {
	case TaskDone, TaskError, TaskCancelled, TaskTimeout:
		return true
	default:
		return false
	}
}

// targetText renders a request's target the way the director's task
// description does.
func targetText(fields map[string]interface{}) string {
	target, err := json.Marshal(fields["target"])
	if err != nil {
		return "{}"
	}

	return string(target)
}

// SSHHostEntry is one entry of a setup task's result output, in the
// director's field names.
type SSHHostEntry struct {
	Status        string      `json:"status"`
	IP            string      `json:"ip"`
	HostPublicKey string      `json:"host_public_key"`
	Job           string      `json:"job"`
	Index         interface{} `json:"index"`
	ID            string      `json:"id"`
	GatewayUser   string      `json:"gateway_user,omitempty"`
	GatewayHost   string      `json:"gateway_host,omitempty"`
}

// DefaultSSHInstanceID is the instance ID the default setup answer gives.
const DefaultSSHInstanceID = "00000000-0000-0000-0000-000000000001"

// SSHSetupOutput renders a setup task's result output.
func SSHSetupOutput(entries ...SSHHostEntry) string {
	output, err := json.Marshal(entries)
	if err != nil {
		panic("boshtest: encoding an SSH setup output failed: " + err.Error())
	}

	return string(output)
}

// SSHSuccess is a success entry for 127.0.0.1 on redis/0 that reports
// hostKey, in authorized-keys form.
func SSHSuccess(hostKey string) SSHHostEntry {
	return SSHHostEntry{Status: "success", IP: "127.0.0.1", HostPublicKey: hostKey, Job: "redis", Index: 0, ID: DefaultSSHInstanceID}
}

// AuthorizedKey renders a host key the way the director reports it, which
// is the VM's ssh_host_rsa_key.pub with its comment.
func AuthorizedKey(key ssh.PublicKey) string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))) + " root@vm"
}

// defaultSetupOutput is one success entry for 127.0.0.1 for the first
// instance the target names, reporting hostKey.
func defaultSetupOutput(fields map[string]interface{}, hostKey string) string {
	target, _ := fields["target"].(map[string]interface{})
	job, _ := target["job"].(string)

	// bosh-cli sends each index as a string and boshapi as a number, and
	// the director answers with a number either way.
	var index interface{}

	if indexes, ok := target["indexes"].([]interface{}); ok && len(indexes) > 0 {
		switch first := indexes[0].(type) {
		case float64:
			index = int(first)
		case string:
			number, err := strconv.Atoi(first)
			if err == nil {
				index = number
			}
		}
	}

	return SSHSetupOutput(SSHHostEntry{
		Status:        "success",
		IP:            "127.0.0.1",
		HostPublicKey: hostKey,
		Job:           job,
		Index:         index,
		ID:            DefaultSSHInstanceID,
	})
}
