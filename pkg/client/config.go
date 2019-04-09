package client

import (
	"crypto/tls"
	"time"
)

type Configuration struct {
	ID string

	Subject string

	// TLS config
	TLSConfig *tls.Config

	// Remote site for websocket address
	Remote string

	// Map of upstream servers dns.entry=http://ip:port
	UpstreamMap map[string]string

	// Token for authentication
	Token string

	// PingWaitDuration duration to wait between pings
	PingWaitDuration time.Duration
}
