package internal

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"time"
)

// sendResetMail delivers the reset-code email over plain SMTP (01 §7,
// net/smtp with STARTTLS when the server offers it). Any error is surfaced to
// handleForgot, which stamps mail_failed on the row — the forgot 200 with
// {sent:true} is never affected (06 §2 E22).
func (s *Server) sendResetMail(to, code string) error {
	if s.cfg.SMTPHost == "" {
		return fmt.Errorf("smtp not configured")
	}
	addr := net.JoinHostPort(s.cfg.SMTPHost, s.cfg.SMTPPort)
	dialer := net.Dialer{Timeout: 10 * time.Second}
	conn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return err
	}
	client, err := smtp.NewClient(conn, s.cfg.SMTPHost)
	if err != nil {
		conn.Close()
		return err
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.cfg.SMTPHost}); err != nil {
			return err
		}
	}
	if s.cfg.SMTPUser != "" {
		auth := smtp.PlainAuth("", s.cfg.SMTPUser, s.cfg.SMTPPass, s.cfg.SMTPHost)
		if err := client.Auth(auth); err != nil {
			return err
		}
	}
	if err := client.Mail(s.cfg.SMTPFrom); err != nil {
		return err
	}
	if err := client.Rcpt(to); err != nil {
		return err
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Atur ulang sandi Kidversa\r\n"+
		"MIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		s.cfg.SMTPFrom, to, resetMailBody(code, int(s.cfg.ResetTTL.Minutes())))
	if _, err := w.Write([]byte(msg)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

// resetMailBody is the Indonesian relay copy for the emailed code.
func resetMailBody(code string, ttlMinutes int) string {
	return fmt.Sprintf(
		"Permintaan atur ulang sandi akun Kidversa.\n\n"+
			"Kode verifikasi: %s\n"+
			"Berlaku %d menit dan hanya dapat dipakai satu kali.\n\n"+
			"Jika Anda tidak meminta email ini, abaikan saja.\n",
		code, ttlMinutes)
}
