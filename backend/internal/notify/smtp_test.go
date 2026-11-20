package notify

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/http/httptest"
	"net/textproto"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danielliu30/dating-coach/backend/internal/config"
)

const secretMailBody = "Your verification code is: 123456; Your password reset code is: 654321; token=secret-token-value"

func captureNotificationLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestNotificationLoggingIsDevelopmentOnly(t *testing.T) {
	for _, env := range []string{"development", "production", "staging", ""} {
		t.Run(env, func(t *testing.T) {
			logs := captureNotificationLogs(t)
			svc := New(&config.Config{Env: env})
			err := svc.Send(context.Background(), Message{To: "to@example.test", Subject: secretMailBody, Body: secretMailBody, ICS: secretMailBody})
			if (err == nil) != (env == "development") {
				t.Fatalf("Send error=%v env=%q", err, env)
			}
			checkSecrets := func() {
				for _, secret := range []string{"123456", "654321", "secret-token-value"} {
					if strings.Contains(logs.String(), secret) != (env == "development") {
						t.Fatalf("env=%q unexpected secret logging: %s", env, logs)
					}
				}
			}
			checkSecrets()
			if env == "development" && !strings.Contains(logs.String(), `"to":"to@example.test"`) {
				t.Fatal("development email logs lost e2e account selector")
			}
			logs.Reset()
			if err := svc.Push(context.Background(), "user-id", secretMailBody, secretMailBody); err != nil {
				t.Fatal(err)
			}
			checkSecrets()
		})
	}
}

type smtpTranscript struct {
	commands []string
	body     string
}

func smtpServer(t *testing.T, mode string, serverTLS *tls.Config) (string, <-chan smtpTranscript) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan smtpTranscript, 1)
	t.Cleanup(func() { cancel(); ln.Close() })
	go func() {
		var transcript smtpTranscript
		defer func() { result <- transcript }()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		defer stop()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		var wire net.Conn = conn
		secured := mode == "implicit"
		if secured {
			wire = tls.Server(conn, serverTLS)
		}
		if mode == "bad-greeting" {
			fmt.Fprintf(wire, "550 %s\r\n", secretMailBody)
			return
		}
		fmt.Fprint(wire, "220 localhost test SMTP\r\n")
		reader := bufio.NewReader(wire)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimSpace(line)
			transcript.commands = append(transcript.commands, line)
			switch {
			case strings.HasPrefix(line, "EHLO"):
				if mode == "starttls" && !secured {
					fmt.Fprint(wire, "250-localhost\r\n250 STARTTLS\r\n")
				} else {
					fmt.Fprint(wire, "250 localhost\r\n")
				}
			case line == "STARTTLS":
				fmt.Fprint(wire, "220 Ready for TLS\r\n")
				wire = tls.Server(conn, serverTLS)
				secured = true
				reader = bufio.NewReader(wire)
			case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
				fmt.Fprint(wire, "250 OK\r\n")
			case line == "DATA":
				fmt.Fprint(wire, "354 End with dot\r\n")
				body, err := textproto.NewReader(reader).ReadDotBytes()
				if err != nil {
					return
				}
				transcript.body = string(body)
				fmt.Fprint(wire, "250 Accepted\r\n")
			case line == "QUIT":
				fmt.Fprint(wire, "221 Bye\r\n")
				return
			default:
				fmt.Fprint(wire, "500 Unexpected command\r\n")
			}
		}
	}()
	return ln.Addr().String(), result
}

func TestSMTPPlaintextPolicy(t *testing.T) {
	for _, tc := range []struct {
		env                  string
		requireTLS, wantSend bool
	}{
		{"development", false, true},
		{"development", true, false},
		{"production", true, false},
		{"production", false, false},
		{"staging", false, false},
		{"", false, false},
	} {
		t.Run(fmt.Sprintf("%s/TLS=%v", tc.env, tc.requireTLS), func(t *testing.T) {
			addr, result := smtpServer(t, "plain", nil)
			host, port, _ := net.SplitHostPort(addr)
			n, _ := strconv.Atoi(port)
			svc := New(&config.Config{Env: tc.env, SMTPHost: host, SMTPPort: n, SMTPTLS: tc.requireTLS, SMTPTimeout: time.Second, MailFrom: "from@example.test"})
			err := svc.Email(context.Background(), "to@example.test", "Code", secretMailBody)
			if (err == nil) != tc.wantSend {
				t.Fatalf("Send error=%v wantSend=%v", err, tc.wantSend)
			}
			transcript := <-result
			if tc.wantSend {
				if !strings.Contains(transcript.body, secretMailBody) {
					t.Fatal("Mailpit-style server did not receive the message")
				}
			} else {
				for _, command := range transcript.commands {
					if !strings.HasPrefix(command, "EHLO") {
						t.Fatalf("sent %q before required TLS", command)
					}
				}
				if transcript.body != "" {
					t.Fatal("message leaked in plaintext")
				}
			}
		})
	}
}

func TestSMTPTransportErrorsDoNotLeakCodes(t *testing.T) {
	logs := captureNotificationLogs(t)
	addr, result := smtpServer(t, "bad-greeting", nil)
	host, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	svc := New(&config.Config{Env: "production", SMTPHost: host, SMTPPort: n, SMTPTimeout: time.Second})
	err := svc.Email(context.Background(), "to@example.test", "Code", secretMailBody)
	if err == nil {
		t.Fatal("invalid SMTP greeting accepted")
	}
	slog.Error("send verification email", "error", err)
	for _, secret := range []string{"123456", "654321", "secret-token-value"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("SMTP error leaked a secret: %s", logs)
		}
	}
	<-result
}

func TestSMTPEncryptedConnections(t *testing.T) {
	fixture := httptest.NewTLSServer(nil)
	serverTLS := fixture.TLS.Clone()
	roots := x509.NewCertPool()
	roots.AddCert(fixture.Certificate())
	fixture.Close()
	for _, mode := range []string{"starttls", "implicit"} {
		for _, trusted := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/trusted=%v", mode, trusted), func(t *testing.T) {
				addr, result := smtpServer(t, mode, serverTLS)
				conn, err := net.DialTimeout("tcp", addr, time.Second)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(5 * time.Second))
				clientTLS := &tls.Config{ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12, RootCAs: x509.NewCertPool()}
				if trusted {
					clientTLS.RootCAs = roots
				}
				client, err := newSMTPClient(context.Background(), conn, clientTLS, mode == "implicit", true)
				if !trusted {
					if err == nil {
						client.Close()
						t.Fatal("untrusted SMTP certificate accepted")
					}
					conn.Close()
					<-result
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				state, ok := client.TLSConnectionState()
				if !ok || !state.HandshakeComplete || len(state.VerifiedChains) == 0 {
					t.Fatal("SMTP connection is not verified TLS")
				}
				if err := client.Quit(); err != nil {
					t.Fatal(err)
				}
				<-result
			})
		}
	}
}
