package broker_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"blacksmith/internal/broker"
	"blacksmith/pkg/logger"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

const (
	valkeyTestInstanceID    = "instance-7f3c"
	valkeyTestBindingID     = "binding-91ad"
	valkeyTestHost          = "127.0.0.1"
	valkeyTestAdminPassword = "admin-pw-do-not-leak" //nolint:gosec // fake credential the tests prove stays out of errors
	valkeyTestUserPassword  = "user-pw-do-not-leak"  //nolint:gosec // fake credential the tests prove stays out of errors
	valkeyMisconfReply      = "-MISCONF Valkey is configured to save RDB snapshots, but it's currently unable to persist to disk. " +
		"Commands that may modify the data set are disabled, because this instance is configured to report errors during " +
		"writes if RDB snapshotting fails (stop-writes-on-bgsave-error option).\r\n"
)

// closeConnection is a reply that makes the fake close the connection without
// answering, the way a node that resets or times out does.
const closeConnection = "close-the-connection"

// fakeValkey is a minimal RESP server standing in for a Valkey node. It
// answers each command with the reply set for it, or +OK, and records the
// commands it received (without their arguments, which carry passwords).
type fakeValkey struct {
	listener net.Listener
	replies  map[string]func(args []string) string

	mu       sync.Mutex
	commands []string
}

func newFakeValkey(replies map[string]func(args []string) string) *fakeValkey {
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", valkeyTestHost+":0")
	Expect(err).NotTo(HaveOccurred())

	fake := &fakeValkey{listener: listener, replies: replies}

	go fake.serve()

	return fake
}

func (f *fakeValkey) serve() {
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			return
		}

		go f.handle(conn)
	}
}

func (f *fakeValkey) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	reader := bufio.NewReader(conn)

	for {
		args, err := readRESPCommand(reader)
		if err != nil {
			return
		}

		name := strings.ToUpper(args[0])
		if name == "ACL" && len(args) > 1 {
			name += " " + strings.ToUpper(args[1])
		}

		f.mu.Lock()
		f.commands = append(f.commands, name)
		f.mu.Unlock()

		reply := "+OK\r\n"
		if replyFn, ok := f.replies[name]; ok {
			reply = replyFn(args)
		}

		if reply == closeConnection {
			return
		}

		_, err = io.WriteString(conn, reply)
		if err != nil {
			return
		}
	}
}

func (f *fakeValkey) port() int {
	addr, ok := f.listener.Addr().(*net.TCPAddr)
	Expect(ok).To(BeTrue())

	return addr.Port
}

func (f *fakeValkey) received() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.commands...)
}

func (f *fakeValkey) close() {
	_ = f.listener.Close()
}

// readRESPCommand reads one command sent as a RESP array of bulk strings.
func readRESPCommand(reader *bufio.Reader) ([]string, error) {
	header, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}

	count, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(header, "*")))
	if err != nil {
		return nil, err
	}

	args := make([]string, 0, count)

	for range count {
		lengthLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}

		length, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(lengthLine, "$")))
		if err != nil {
			return nil, err
		}

		buf := make([]byte, length+2)

		_, err = io.ReadFull(reader, buf)
		if err != nil {
			return nil, err
		}

		args = append(args, string(buf[:length]))
	}

	return args, nil
}

func fixedReply(reply string) func([]string) string {
	return func([]string) string { return reply }
}

// recordingLogger keeps every message logged at error level, formatted the
// way the broker's printf-style logger formats them.
type recordingLogger struct {
	*logger.NoOpLogger

	mu     sync.Mutex
	errors []string
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{NoOpLogger: &logger.NoOpLogger{}}
}

func (r *recordingLogger) Error(msg string, args ...interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.errors = append(r.errors, fmt.Sprintf(msg, args...))
}

func (r *recordingLogger) errorLines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]string(nil), r.errors...)
}

func valkeyCredMap(port int) map[string]interface{} {
	return map[string]interface{}{
		"service_type":   "valkey",
		"host":           valkeyTestHost,
		"port":           port,
		"admin_password": valkeyTestAdminPassword,
	}
}

var _ = Describe("Valkey ACL admin failures", func() {
	var (
		brokerInstance *broker.Broker
		recorder       *recordingLogger
		restoreRetry   func()
		ctx            context.Context
	)

	BeforeEach(func() {
		brokerInstance = &broker.Broker{}
		recorder = newRecordingLogger()
		restoreRetry = broker.SetValkeyACLRetry(1, time.Millisecond)
		ctx = context.Background()
	})

	AfterEach(func() {
		restoreRetry()
	})

	Context("when the node answers PING with MISCONF", func() {
		var fake *fakeValkey

		BeforeEach(func() {
			fake = newFakeValkey(map[string]func([]string) string{"PING": fixedReply(valkeyMisconfReply)})
		})

		AfterEach(func() {
			fake.close()
		})

		It("names the PING step and address and explains the likely cause on unbind", func() {
			addr := fmt.Sprintf("%s:%d", valkeyTestHost, fake.port())

			err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

			Expect(errors.Is(err, broker.ErrFailedToDeleteValkeyACLUser)).To(BeTrue())
			Expect(err.Error()).To(HavePrefix("failed to delete Valkey ACL user " + valkeyTestBindingID + ": "))
			Expect(err.Error()).To(ContainSubstring("valkey PING " + addr + " (plaintext) failed: MISCONF Valkey is configured to save RDB snapshots"))
			Expect(err.Error()).To(ContainSubstring("likely cause: the Valkey node cannot persist to disk"))
			Expect(err.Error()).To(ContainSubstring("check the node's server log and free space on its persistent disk"))
			Expect(err.Error()).NotTo(ContainSubstring(valkeyTestAdminPassword))
			Expect(fake.received()).To(Equal([]string{"AUTH", "PING"}))
		})

		It("logs every failed attempt with the instance, binding, address, transport, and cause", func() {
			addr := fmt.Sprintf("%s:%d", valkeyTestHost, fake.port())

			err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)
			Expect(err).To(HaveOccurred())

			lines := recorder.errorLines()
			Expect(lines).To(HaveLen(1))

			for _, line := range lines {
				Expect(line).To(ContainSubstring("Failed to delete Valkey ACL user for instance " + valkeyTestInstanceID + " binding " + valkeyTestBindingID))
				Expect(line).To(ContainSubstring("at " + addr + " (tls=false)"))
				Expect(line).To(ContainSubstring("attempt 1 of 2, not retrying"))
				Expect(line).To(ContainSubstring("valkey PING " + addr + " (plaintext) failed: MISCONF"))
				Expect(line).NotTo(ContainSubstring(valkeyTestAdminPassword))
			}
		})

		It("names the PING step and explains the likely cause on bind", func() {
			err := brokerInstance.CreateValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, valkeyTestUserPassword, recorder)

			Expect(errors.Is(err, broker.ErrFailedToCreateValkeyACLUser)).To(BeTrue())
			Expect(err.Error()).To(ContainSubstring("valkey PING " + valkeyTestHost + ":"))
			Expect(err.Error()).To(ContainSubstring("likely cause: the Valkey node cannot persist to disk"))
			Expect(recorder.errorLines()).To(HaveLen(1))
			Expect(recorder.errorLines()[0]).To(ContainSubstring("Failed to create Valkey ACL user for instance " + valkeyTestInstanceID))
		})

		It("reports which nodes failed and how many for a cluster", func() {
			credMap := valkeyCredMap(fake.port())
			credMap["hosts"] = []interface{}{valkeyTestHost, valkeyTestHost}

			err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, credMap, valkeyTestBindingID, recorder)

			Expect(errors.Is(err, broker.ErrFailedToDeleteValkeyACLUser)).To(BeTrue())
			Expect(err.Error()).To(HavePrefix("failed to delete Valkey ACL user " + valkeyTestBindingID + " on 2 of 2 cluster nodes: "))
			Expect(strings.Count(err.Error(), "MISCONF Valkey")).To(Equal(2))
			Expect(recorder.errorLines()).To(HaveLen(2))
			Expect(fake.received()).To(Equal([]string{"AUTH", "PING", "AUTH", "PING"}))
			Expect(err.Error()).NotTo(ContainSubstring("max retries exceeded"))
		})

		It("makes exactly one attempt even with retries configured", func() {
			restoreRetry()
			restoreRetry = broker.SetValkeyACLRetry(3, time.Millisecond)

			err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).NotTo(ContainSubstring("max retries exceeded"))
			Expect(fake.received()).To(Equal([]string{"AUTH", "PING"}))
		})
	})

	for _, code := range []string{"NOAUTH Authentication required.", "NOPERM this user has no permissions to run the 'auth' command", "WRONGPASS invalid username-password pair or user is disabled."} {
		code := code

		It("makes one attempt when the node answers AUTH with "+strings.Fields(code)[0], func() {
			restoreRetry()
			restoreRetry = broker.SetValkeyACLRetry(3, time.Millisecond)

			fake := newFakeValkey(map[string]func([]string) string{"AUTH": fixedReply("-" + code + "\r\n")})
			defer fake.close()

			err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).NotTo(ContainSubstring("max retries exceeded"))
			Expect(fake.received()).To(Equal([]string{"AUTH"}))
		})
	}

	It("makes four attempts when the connection closes before AUTH is answered", func() {
		restoreRetry()
		restoreRetry = broker.SetValkeyACLRetry(3, time.Millisecond)

		fake := newFakeValkey(map[string]func([]string) string{"AUTH": fixedReply(closeConnection)})
		defer fake.close()

		err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("max retries exceeded"))
		Expect(fake.received()).To(Equal([]string{"AUTH", "AUTH", "AUTH", "AUTH"}))
		Expect(recorder.errorLines()).To(HaveLen(4))
	})

	for _, reply := range []string{
		"ERR max number of clients reached",
		"ERR AUTH <password> called without any password configured for the default user. Are you sure your configuration is correct?",
	} {
		reply := reply

		It("keeps retrying and does not blame the password when AUTH answers "+reply[:24], func() {
			restoreRetry()
			restoreRetry = broker.SetValkeyACLRetry(3, time.Millisecond)

			fake := newFakeValkey(map[string]func([]string) string{"AUTH": fixedReply("-" + reply + "\r\n")})
			defer fake.close()

			err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("max retries exceeded"))
			Expect(err.Error()).NotTo(ContainSubstring("admin_password does not match"))
			Expect(fake.received()).To(Equal([]string{"AUTH", "AUTH", "AUTH", "AUTH"}))
		})
	}

	It("says the node is at maxclients when AUTH answers that", func() {
		fake := newFakeValkey(map[string]func([]string) string{"AUTH": fixedReply("-ERR max number of clients reached\r\n")})
		defer fake.close()

		err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("likely cause: the node is at its maxclients limit"))
	})

	It("makes one attempt when the node answers ACL SETUSER with NOPERM", func() {
		restoreRetry()
		restoreRetry = broker.SetValkeyACLRetry(3, time.Millisecond)

		fake := newFakeValkey(map[string]func([]string) string{
			"ACL SETUSER": fixedReply("-NOPERM User default has no permissions to run the 'acl|setuser' command\r\n"),
		})
		defer fake.close()

		err := brokerInstance.CreateValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, valkeyTestUserPassword, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).NotTo(ContainSubstring("max retries exceeded"))
		Expect(fake.received()).To(Equal([]string{"AUTH", "PING", "ACL SETUSER"}))
	})

	It("makes one attempt when the node answers PING with NOAUTH", func() {
		restoreRetry()
		restoreRetry = broker.SetValkeyACLRetry(3, time.Millisecond)

		fake := newFakeValkey(map[string]func([]string) string{"PING": fixedReply("-NOAUTH Authentication required.\r\n")})
		defer fake.close()

		err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

		Expect(err).To(HaveOccurred())
		Expect(fake.received()).To(Equal([]string{"AUTH", "PING"}))
	})

	It("explains NOPERM on ACL SETUSER with the missing permission and where to look", func() {
		fake := newFakeValkey(map[string]func([]string) string{
			"ACL SETUSER": fixedReply("-NOPERM User default has no permissions to run the 'acl|setuser' command\r\n"),
		})
		defer fake.close()

		err := brokerInstance.CreateValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, valkeyTestUserPassword, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("valkey ACL SETUSER"))
		Expect(err.Error()).To(ContainSubstring("likely cause: the admin user lacks ACL permission to run this command"))
		Expect(err.Error()).To(ContainSubstring("check the 'user default' line"))
	})

	It("explains NOAUTH with the credential to check", func() {
		fake := newFakeValkey(map[string]func([]string) string{"PING": fixedReply("-NOAUTH Authentication required.\r\n")})
		defer fake.close()

		err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("likely cause: the node requires a password and the broker did not authenticate"))
		Expect(err.Error()).To(ContainSubstring("check that admin_password is present"))
	})

	It("explains an ACL SAVE failure with the ACL file directory to check", func() {
		fake := newFakeValkey(map[string]func([]string) string{
			"ACL SAVE": fixedReply("-ERR There was an error trying to save the ACLs. Please check the server logs for more information\r\n"),
		})
		defer fake.close()

		err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("valkey ACL SAVE"))
		Expect(err.Error()).To(ContainSubstring("likely cause: the node could not write its ACL file"))
		Expect(err.Error()).To(ContainSubstring("check the aclfile path and its directory permissions"))
	})

	It("names the dial step and points at reachability when nothing listens", func() {
		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", valkeyTestHost+":0")
		Expect(err).NotTo(HaveOccurred())

		addr, ok := listener.Addr().(*net.TCPAddr)
		Expect(ok).To(BeTrue())
		Expect(listener.Close()).To(Succeed())

		err = brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(addr.Port), valkeyTestBindingID, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(fmt.Sprintf("valkey dial %s:%d (plaintext) failed: ", valkeyTestHost, addr.Port)))
		Expect(err.Error()).To(ContainSubstring("likely cause: the node is down, unreachable from the broker, or not listening on this port"))
		Expect(recorder.errorLines()).To(HaveLen(2))
		Expect(recorder.errorLines()[1]).To(ContainSubstring("attempt 2 of 2"))
	})

	It("still makes four attempts when the dial is refused", func() {
		restoreRetry()
		restoreRetry = broker.SetValkeyACLRetry(3, time.Millisecond)

		listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", valkeyTestHost+":0")
		Expect(err).NotTo(HaveOccurred())

		addr, ok := listener.Addr().(*net.TCPAddr)
		Expect(ok).To(BeTrue())
		Expect(listener.Close()).To(Succeed())

		err = brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(addr.Port), valkeyTestBindingID, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("max retries exceeded"))
		Expect(recorder.errorLines()).To(HaveLen(4))
		Expect(recorder.errorLines()[3]).To(ContainSubstring("attempt 4 of 4"))
	})

	It("names the AUTH step and points at the admin password when AUTH is rejected", func() {
		fake := newFakeValkey(map[string]func([]string) string{
			"AUTH": fixedReply("-WRONGPASS invalid username-password pair or user is disabled.\r\n"),
		})
		defer fake.close()

		err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(fmt.Sprintf("valkey AUTH %s:%d (plaintext) failed: WRONGPASS", valkeyTestHost, fake.port())))
		Expect(err.Error()).To(ContainSubstring("likely cause: the instance's admin_password does not match"))
		Expect(err.Error()).NotTo(ContainSubstring(valkeyTestAdminPassword))
		Expect(fake.received()).To(Equal([]string{"AUTH"}))
		Expect(recorder.errorLines()).To(HaveLen(1))
	})

	It("redacts the binding password when Valkey echoes a rejected SETUSER modifier", func() {
		fake := newFakeValkey(map[string]func([]string) string{
			"ACL SETUSER": func(args []string) string {
				return "-ERR Error in ACL SETUSER modifier '" + args[4] + "': Syntax error\r\n"
			},
		})
		defer fake.close()

		err := brokerInstance.CreateValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, valkeyTestUserPassword, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("valkey ACL SETUSER " + valkeyTestHost + ":"))
		Expect(err.Error()).To(ContainSubstring("modifier '>[REDACTED]'"))
		Expect(err.Error()).NotTo(ContainSubstring(valkeyTestUserPassword))

		for _, line := range recorder.errorLines() {
			Expect(line).NotTo(ContainSubstring(valkeyTestUserPassword))
		}
	})

	It("names the ACL SAVE step when the node cannot persist the ACL file", func() {
		fake := newFakeValkey(map[string]func([]string) string{
			"ACL DELUSER": fixedReply(":1\r\n"),
			"ACL SAVE":    fixedReply("-ERR There was an error trying to save the ACLs. Please check the server logs for more information\r\n"),
		})
		defer fake.close()

		err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring(fmt.Sprintf("valkey ACL SAVE %s:%d (plaintext) failed: ERR There was an error trying to save the ACLs", valkeyTestHost, fake.port())))
	})

	It("deletes the user and saves the ACL file without logging an error when the node is healthy", func() {
		fake := newFakeValkey(map[string]func([]string) string{"ACL DELUSER": fixedReply(":1\r\n")})
		defer fake.close()

		err := brokerInstance.DeleteValkeyACLUser(ctx, valkeyTestInstanceID, valkeyCredMap(fake.port()), valkeyTestBindingID, recorder)

		Expect(err).NotTo(HaveOccurred())
		Expect(fake.received()).To(Equal([]string{"AUTH", "PING", "ACL DELUSER", "ACL SAVE"}))
		Expect(recorder.errorLines()).To(BeEmpty())
	})
})
