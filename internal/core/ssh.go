package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func (s *Session) connectSSH(ctx context.Context, o Options) error {
	if o.User == "" {
		return errors.New("SSH user is required")
	}
	var host ssh.HostKeyCallback
	switch {
	case o.Insecure:
		host = ssh.InsecureIgnoreHostKey()
	case o.HostKey != "":
		host = func(_ string, _ net.Addr, k ssh.PublicKey) error {
			if ssh.FingerprintSHA256(k) != o.HostKey {
				return errors.New("SSH host key fingerprint mismatch")
			}
			return nil
		}
	case o.KnownHosts != "":
		var e error
		host, e = knownhosts.New(o.KnownHosts)
		if e != nil {
			return e
		}
	default:
		return errors.New("SSH requires known_hosts or host_key SHA256 fingerprint; insecure=true is explicit opt-in")
	}
	auth := []ssh.AuthMethod{}
	if o.Password != "" {
		auth = append(auth, ssh.Password(o.Password))
	}
	if o.KeyFile != "" {
		b, e := os.ReadFile(o.KeyFile)
		if e != nil {
			return e
		}
		var key ssh.Signer
		if o.KeyPassphrase != "" {
			key, e = ssh.ParsePrivateKeyWithPassphrase(b, []byte(o.KeyPassphrase))
		} else {
			key, e = ssh.ParsePrivateKey(b)
		}
		if e != nil {
			return e
		}
		auth = append(auth, ssh.PublicKeys(key))
	}
	if len(auth) == 0 {
		return errors.New("SSH password or key_file required")
	}
	c, e := (&net.Dialer{Timeout: IOLimit}).DialContext(ctx, "tcp", o.Address)
	if e != nil {
		return e
	}
	s.closer = c
	deadline := time.Now().Add(IOLimit)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = c.SetDeadline(deadline)
	// Closing the raw socket interrupts handshake, channel setup, writes and Close.
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	cc, chans, reqs, e := ssh.NewClientConn(c, o.Address, &ssh.ClientConfig{User: o.User, Auth: auth, HostKeyCallback: host, Timeout: IOLimit})
	if e != nil {
		return e
	}
	client := ssh.NewClient(cc, chans, reqs)
	if s.interactive {
		sh, e := client.NewSession()
		if e != nil {
			return e
		}
		sh.Stdout = s
		sh.Stderr = s
		in, e := sh.StdinPipe()
		if e != nil {
			return e
		}
		s.input = in
		if e = sh.RequestPty("vt100", 24, 80, ssh.TerminalModes{ssh.ECHO: 0, ssh.TTY_OP_ISPEED: 9600, ssh.TTY_OP_OSPEED: 9600}); e != nil {
			return e
		}
		if e = sh.Shell(); e != nil {
			return e
		}
		go func() {
			e := sh.Wait()
			why := "remote shell closed"
			if e != nil {
				why = e.Error()
			}
			s.disconnect(why)
		}()
	} else {
		s.exec = func(command string) error {
			sh, e := client.NewSession()
			if e != nil {
				return e
			}
			sh.Stdout = s
			sh.Stderr = s
			if e = sh.Start(command); e != nil {
				_ = sh.Close()
				return e
			}
			go func() {
				e := sh.Wait()
				_ = sh.Close()
				code := 0
				var exit *ssh.ExitError
				var missing *ssh.ExitMissingError
				if e != nil {
					if errors.As(e, &missing) {
						s.mu.Lock()
						s.exit = nil
						s.exitMissing = true
						s.warning = "SSH channel closed without exit-status/exit-signal; command outcome is unknown"
						s.running = false
						s.mu.Unlock()
						s.signal()
						return
					} else if errors.As(e, &exit) {
						code = exit.ExitStatus()
					} else {
						s.disconnect(fmt.Sprintf("SSH command transport error: %v; reconnect", e))
						return
					}
				}
				s.mu.Lock()
				s.exit = &code
				s.running = false
				s.mu.Unlock()
				s.signal()
			}()
			return nil
		}
	}
	_ = c.SetDeadline(time.Time{})
	go func() {
		e := client.Wait()
		if e != nil {
			s.disconnect("SSH disconnected: " + e.Error())
		} else {
			s.disconnect("SSH disconnected")
		}
	}()
	return nil
}
