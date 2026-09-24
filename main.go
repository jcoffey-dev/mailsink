// SPDX-License-Identifier: GPL-3.0-or-later

// mailsink is an SMTP server for test and development networks. It accepts
// mail for an allowlist of domains and throws it away: message bodies are
// read off the socket and discarded, never buffered or written to disk, and
// the program has no code that opens an outbound connection, so nothing it
// receives can be relayed. It writes no logs.
//
// All configuration is compiled in at build time (see the Dockerfile); the
// running binary reads no environment variables and no files.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// Set by the Dockerfile through -ldflags -X.
var (
	allowedDomains  = ""
	allowedNetworks = "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"
	hostname        = "mailsink.test"
	port            = "25"
	maxMessageSize  = "26214400"
	maxRecipients   = "100"
	maxConnections  = "100"
)

const (
	commandTimeout = 5 * time.Minute
	dataTimeout    = 10 * time.Minute
	maxLineLength  = 4096
	maxErrors      = 10
)

type config struct {
	anyDomain      bool
	domains        map[string]bool
	suffixes       []string
	networks       []netip.Prefix
	hostname       string
	addr           string
	maxMessageSize int64
	maxRecipients  int
	maxConnections int
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		out = append(out, strings.ToLower(strings.TrimSpace(f)))
	}
	return out
}

func positive(name, v string) (int64, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", name, v)
	}
	return n, nil
}

func loadConfig() (*config, error) {
	c := &config{domains: map[string]bool{}, hostname: hostname}

	for _, d := range splitList(allowedDomains) {
		d = strings.TrimSuffix(d, ".")
		switch {
		case d == "*":
			c.anyDomain = true
		case strings.HasPrefix(d, "*."):
			c.suffixes = append(c.suffixes, d[1:])
		case d != "":
			c.domains[d] = true
		}
	}
	if !c.anyDomain && len(c.domains) == 0 && len(c.suffixes) == 0 {
		return nil, errors.New("ALLOWED_DOMAINS is empty")
	}

	// Loopback is always allowed so the container healthcheck works.
	c.networks = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	for _, n := range splitList(allowedNetworks) {
		p, err := netip.ParsePrefix(n)
		if err != nil {
			return nil, fmt.Errorf("ALLOWED_NETWORKS: %v", err)
		}
		c.networks = append(c.networks, p.Masked())
	}

	p, err := positive("SMTP_PORT", port)
	if err != nil || p > 65535 {
		return nil, fmt.Errorf("SMTP_PORT must be 1-65535, got %q", port)
	}
	c.addr = ":" + port

	if c.maxMessageSize, err = positive("MAX_MESSAGE_SIZE", maxMessageSize); err != nil {
		return nil, err
	}
	n, err := positive("MAX_RECIPIENTS", maxRecipients)
	if err != nil {
		return nil, err
	}
	c.maxRecipients = int(n)
	if n, err = positive("MAX_CONNECTIONS", maxConnections); err != nil {
		return nil, err
	}
	c.maxConnections = int(n)
	if c.hostname == "" {
		return nil, errors.New("SMTP_HOSTNAME is empty")
	}
	return c, nil
}

func (c *config) clientAllowed(remote net.Addr) bool {
	ap, err := netip.ParseAddrPort(remote.String())
	if err != nil {
		return false
	}
	a := ap.Addr().Unmap()
	for _, p := range c.networks {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (c *config) domainAllowed(d string) bool {
	d = strings.TrimSuffix(strings.ToLower(d), ".")
	if c.anyDomain || c.domains[d] {
		return true
	}
	for _, s := range c.suffixes {
		if strings.HasSuffix(d, s) {
			return true
		}
	}
	return false
}

func main() {
	check := flag.Bool("check", false, "validate the compiled-in configuration, print it and exit")
	health := flag.Bool("healthcheck", false, "connect to the local server and exit 0 if it answers")
	flag.Parse()

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "mailsink: configuration:", err)
		os.Exit(2)
	}

	switch {
	case *check:
		fmt.Printf("hostname:          %s\nlisten:            %s\nallowed domains:   %s\nallowed networks:  %s\nmax message size:  %d\nmax recipients:    %d\nmax connections:   %d\n",
			cfg.hostname, cfg.addr, allowedDomains, allowedNetworks, cfg.maxMessageSize, cfg.maxRecipients, cfg.maxConnections)
		return
	case *health:
		os.Exit(healthcheck(cfg))
	}

	ln, err := net.Listen("tcp", cfg.addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mailsink:", err)
		os.Exit(1)
	}
	slots := make(chan struct{}, cfg.maxConnections)
	for {
		conn, err := ln.Accept()
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		select {
		case slots <- struct{}{}:
			go func() {
				defer func() { <-slots }()
				serve(cfg, conn)
			}()
		default:
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			conn.Write([]byte("421 4.3.2 Too many connections, try again later\r\n"))
			conn.Close()
		}
	}
}

func healthcheck(cfg *config) int {
	conn, err := net.DialTimeout("tcp", "127.0.0.1"+cfg.addr, 3*time.Second)
	if err != nil {
		return 1
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "220") {
		return 1
	}
	conn.Write([]byte("QUIT\r\n"))
	return 0
}

type session struct {
	cfg        *config
	conn       net.Conn
	r          *bufio.Reader
	w          *bufio.Writer
	greeted    bool
	mailFrom   bool
	recipients int
	errors     int
}

func serve(cfg *config, conn net.Conn) {
	defer conn.Close()
	s := &session{cfg: cfg, conn: conn, r: bufio.NewReaderSize(conn, maxLineLength), w: bufio.NewWriter(conn)}

	if !cfg.clientAllowed(conn.RemoteAddr()) {
		s.reply("554 5.7.1 Access denied")
		return
	}
	s.reply("220 " + cfg.hostname + " ESMTP mailsink; all mail is discarded")

	for {
		line, tooLong, err := s.readLine(commandTimeout)
		if err != nil {
			return
		}
		if tooLong {
			if !s.fail("500 5.5.2 Line too long") {
				return
			}
			continue
		}
		verb, arg, _ := strings.Cut(line, " ")
		arg = strings.TrimSpace(arg)
		switch strings.ToUpper(verb) {
		case "EHLO":
			if arg == "" {
				s.fail("501 5.5.4 EHLO requires a domain")
				continue
			}
			s.reset()
			s.greeted = true
			s.reply("250-"+s.cfg.hostname,
				"250-PIPELINING",
				"250-SIZE "+strconv.FormatInt(s.cfg.maxMessageSize, 10),
				"250-8BITMIME",
				"250-ENHANCEDSTATUSCODES",
				"250-AUTH PLAIN LOGIN",
				"250 SMTPUTF8")
		case "HELO":
			if arg == "" {
				s.fail("501 5.5.4 HELO requires a domain")
				continue
			}
			s.reset()
			s.greeted = true
			s.reply("250 " + s.cfg.hostname)
		case "AUTH":
			if !s.auth(arg) {
				return
			}
		case "MAIL":
			s.mail(arg)
		case "RCPT":
			s.rcpt(arg)
		case "DATA":
			if !s.data() {
				return
			}
		case "RSET":
			s.reset()
			s.reply("250 2.0.0 OK")
		case "NOOP":
			s.reply("250 2.0.0 OK")
		case "VRFY":
			s.reply("252 2.1.5 Cannot verify user")
		case "HELP":
			s.reply("214 2.0.0 mailsink accepts mail for its allowed domains and discards it")
		case "QUIT":
			s.reply("221 2.0.0 Bye")
			return
		case "STARTTLS", "BDAT", "ETRN", "EXPN", "TURN":
			if !s.fail("502 5.5.1 Command not implemented") {
				return
			}
		default:
			if !s.fail("500 5.5.2 Command not recognized") {
				return
			}
		}
	}
}

func (s *session) reply(lines ...string) {
	s.conn.SetWriteDeadline(time.Now().Add(commandTimeout))
	for _, l := range lines {
		s.w.WriteString(l)
		s.w.WriteString("\r\n")
	}
	s.w.Flush()
}

// fail sends an error reply and returns false once the client has made too
// many errors, after telling it the connection is being closed.
func (s *session) fail(msg string) bool {
	s.reply(msg)
	s.errors++
	if s.errors >= maxErrors {
		s.reply("421 4.7.0 Too many errors, closing connection")
		return false
	}
	return true
}

func (s *session) reset() {
	s.mailFrom = false
	s.recipients = 0
}

// readLine returns one line without its line ending. A line longer than the
// buffer is drained and reported as tooLong.
func (s *session) readLine(timeout time.Duration) (line string, tooLong bool, err error) {
	for {
		s.conn.SetReadDeadline(time.Now().Add(timeout))
		chunk, rerr := s.r.ReadSlice('\n')
		if errors.Is(rerr, bufio.ErrBufferFull) {
			tooLong = true
			continue
		}
		if rerr != nil {
			return "", false, rerr
		}
		if tooLong {
			return "", true, nil
		}
		return strings.TrimRight(string(chunk), "\r\n"), false, nil
	}
}

// parsePath extracts the address from "FROM:<addr> PARAMS" or "TO:<addr>",
// returning the address and whatever parameters follow it.
func parsePath(arg, prefix string) (addr, params string, ok bool) {
	if len(arg) < len(prefix) || !strings.EqualFold(arg[:len(prefix)], prefix) {
		return "", "", false
	}
	rest := strings.TrimSpace(arg[len(prefix):])
	if !strings.HasPrefix(rest, "<") {
		return "", "", false
	}
	end := strings.IndexByte(rest, '>')
	if end < 0 {
		return "", "", false
	}
	addr = rest[1:end]
	// Drop an obsolete source route: <@a,@b:user@example.test>
	if strings.HasPrefix(addr, "@") {
		if i := strings.IndexByte(addr, ':'); i >= 0 {
			addr = addr[i+1:]
		}
	}
	return addr, strings.TrimSpace(rest[end+1:]), true
}

func (s *session) auth(arg string) bool {
	// Any credentials are accepted so applications configured with a
	// username and password work unchanged. They are never looked at.
	mech, initial, _ := strings.Cut(arg, " ")
	var prompts []string
	switch strings.ToUpper(mech) {
	case "PLAIN":
		if initial == "" {
			prompts = []string{"334 "}
		}
	case "LOGIN":
		prompts = []string{"334 VXNlcm5hbWU6", "334 UGFzc3dvcmQ6"}
		if initial != "" {
			prompts = prompts[1:]
		}
	default:
		return s.fail("504 5.5.4 Unrecognized authentication mechanism")
	}
	for _, p := range prompts {
		s.reply(p)
		line, _, err := s.readLine(commandTimeout)
		if err != nil {
			return false
		}
		if line == "*" {
			return s.fail("501 5.0.0 Authentication cancelled")
		}
	}
	s.reply("235 2.7.0 Authentication successful")
	return true
}

func (s *session) mail(arg string) {
	if !s.greeted {
		s.fail("503 5.5.1 Send EHLO or HELO first")
		return
	}
	if s.mailFrom {
		s.fail("503 5.5.1 Sender already given")
		return
	}
	_, params, ok := parsePath(arg, "FROM:")
	if !ok {
		s.fail("501 5.5.4 Syntax: MAIL FROM:<address>")
		return
	}
	for _, p := range strings.Fields(params) {
		k, v, _ := strings.Cut(p, "=")
		if strings.EqualFold(k, "SIZE") {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > s.cfg.maxMessageSize {
				s.reply("552 5.3.4 Message size exceeds fixed limit")
				return
			}
		}
	}
	s.mailFrom = true
	s.reply("250 2.1.0 OK")
}

func (s *session) rcpt(arg string) {
	if !s.mailFrom {
		s.fail("503 5.5.1 Need MAIL before RCPT")
		return
	}
	addr, _, ok := parsePath(arg, "TO:")
	if !ok || addr == "" {
		s.fail("501 5.5.4 Syntax: RCPT TO:<address>")
		return
	}
	if s.recipients >= s.cfg.maxRecipients {
		s.reply("452 4.5.3 Too many recipients")
		return
	}
	at := strings.LastIndexByte(addr, '@')
	if at < 0 {
		// RFC 5321 requires accepting mail for a bare postmaster.
		if !strings.EqualFold(addr, "postmaster") {
			s.fail("501 5.1.3 Recipient address must include a domain")
			return
		}
	} else if !s.cfg.domainAllowed(addr[at+1:]) {
		s.fail("550 5.7.1 Relaying denied: domain not accepted here")
		return
	}
	s.recipients++
	s.reply("250 2.1.5 OK")
}

// data reads the message body and discards it as it arrives. It returns
// false if the connection should be closed.
func (s *session) data() bool {
	if s.recipients == 0 {
		return s.fail("503 5.5.1 Need RCPT before DATA")
	}
	s.reply("354 End data with <CR><LF>.<CR><LF>")

	var size int64
	atLineStart := true
	for {
		s.conn.SetReadDeadline(time.Now().Add(dataTimeout))
		chunk, err := s.r.ReadSlice('\n')
		complete := err == nil
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) {
			return false
		}
		if atLineStart && complete && (string(chunk) == ".\r\n" || string(chunk) == ".\n") {
			break
		}
		size += int64(len(chunk))
		atLineStart = complete
	}

	s.reset()
	if size > s.cfg.maxMessageSize {
		s.reply("552 5.3.4 Message size exceeds fixed limit")
		return true
	}
	s.reply("250 2.0.0 OK: message accepted and discarded")
	return true
}
