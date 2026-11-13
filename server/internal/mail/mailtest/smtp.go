// Package mailtest is a stand-in SMTP server for tests of the mailers and
// the digests. It lives beside the mailer, not in testenv, so the mail
// tests need nothing but the standard library.
package mailtest

import (
	"bufio"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// SMTPServer is a tiny SMTP server on loopback for the mail and digest
// tests: it speaks just enough SMTP for
// net/smtp, keeps every email it was handed in full, and can misbehave the
// ways a real one does. Mode:
//
//	"ok"               accepts everything
//	"refuse_rcpt"      550 to the address
//	"refuse_data"      554 after the email
//	"drop_after_data"  hangs up once it has the email, before answering
//	"quiet_after_data" has the email, never answers
//	"bad_quit"         250 to the email, then 421 to QUIT
type SMTPServer struct {
	Port int
	ln   net.Listener
	mode string
	mu   sync.Mutex
	got  []string
	wg   sync.WaitGroup
}

// Start starts a server in mode; it stops when the test ends.
func Start(t testing.TB, mode string) *SMTPServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &SMTPServer{ln: ln, mode: mode, Port: ln.Addr().(*net.TCPAddr).Port}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(func() { _ = ln.Close(); s.wg.Wait() })
	return s
}

// SetMode changes how later connections are answered.
func (s *SMTPServer) SetMode(mode string) {
	s.mu.Lock()
	s.mode = mode
	s.mu.Unlock()
}

// Received is every email handed over in full, in order.
func (s *SMTPServer) Received() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

// Env is the settings that point the mailer here.
func (s *SMTPServer) Env() map[string]string {
	return map[string]string{"SMTP_HOST": "127.0.0.1", "SMTP_PORT": strconv.Itoa(s.Port), "SMTP_STARTTLS": "false",
		"SMTP_FROM": "Movie Night <mn@example.org>"}
}

func (s *SMTPServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer conn.Close()
			s.talk(conn)
		}()
	}
}

func (s *SMTPServer) talk(conn net.Conn) {
	s.mu.Lock()
	mode := s.mode
	s.mu.Unlock()
	r := bufio.NewReader(conn)
	say := func(lines ...string) { _, _ = conn.Write([]byte(strings.Join(lines, "\r\n") + "\r\n")) }
	say("220 test ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250-test", "250 8BITMIME")
		case strings.HasPrefix(cmd, "HELO"), strings.HasPrefix(cmd, "MAIL"), cmd == "RSET", cmd == "NOOP":
			say("250 OK")
		case strings.HasPrefix(cmd, "RCPT"):
			if mode == "refuse_rcpt" {
				say("550 No such person here")
			} else {
				say("250 OK")
			}
		case cmd == "DATA":
			say("354 Go ahead")
			var body strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil || l == ".\r\n" {
					break
				}
				body.WriteString(l)
			}
			s.mu.Lock()
			s.got = append(s.got, body.String())
			s.mu.Unlock()
			switch mode {
			case "drop_after_data":
				return
			case "quiet_after_data":
				for { // until the client gives up and closes
					if _, err := r.ReadString('\n'); err != nil {
						return
					}
				}
			case "refuse_data":
				say("554 Rejected as spam")
			default:
				say("250 Queued")
			}
		case cmd == "QUIT":
			if mode == "bad_quit" {
				say("421 Going away")
			} else {
				say("221 Bye")
			}
			return
		default:
			say("502 Not here")
		}
	}
}
