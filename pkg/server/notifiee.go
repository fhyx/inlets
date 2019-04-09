package server

type Notifiee interface {
	Connected(guid string, subject string)
	Disconnected(guid string)
}
