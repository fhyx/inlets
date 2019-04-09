package server

import (
	"crypto/tls"
	"time"
)

type Configuration struct {
	TLSConfig      *tls.Config
	GatewayTimeout time.Duration
	Addr           string
	Token          string
}
