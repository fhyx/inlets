package server

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"expvar"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"fhyx/inlets/pkg/client"
	"fhyx/inlets/pkg/transport"
	"fhyx/inlets/pkg/types"

	"github.com/gorilla/websocket"
	"github.com/twinj/uuid"
)

var (
	count = expvar.NewInt("count")
	guids = expvar.NewMap("guids")
)

func New(cfg *Configuration) *Server {
	return &Server{
		cfg:  cfg,
		stop: make(chan struct{}),
	}
}

// Server for the exit-node of inlets
type Server struct {
	cfg *Configuration

	notifs struct {
		sync.RWMutex
		m map[Notifiee]struct{}
	}

	stop chan struct{}
}

// Serve traffic
func (s *Server) Serve() {
	bus := types.NewBus()

	outgoingBus := types.NewRequestBus()

	router := http.NewServeMux()
	router.Handle("/debug/vars", expvar.Handler())
	router.HandleFunc("/", proxyHandler(outgoingBus, bus, s.cfg.GatewayTimeout))
	router.HandleFunc("/tunnel", s.serveWs(outgoingBus, bus))

	svr := http.Server{
		Handler: router,
		Addr:    s.cfg.Addr,
	}

	collectInterval := time.Second * 10
	go garbageCollectBus(bus, collectInterval, s.cfg.GatewayTimeout*2)

	// New listener
	var (
		lerr error
		l    net.Listener
	)
	if s.cfg.TLSConfig != nil {
		l, lerr = tls.Listen("tcp", svr.Addr, s.cfg.TLSConfig)
	} else {
		l, lerr = net.Listen("tcp", svr.Addr)
	}
	if lerr != nil {
		log.Fatal("create listener ERR:%s", lerr)
	}

	if err := svr.Serve(l); err != nil {
		log.Fatal(err)
	}
}

func (s *Server) Stop() {
	//TODO gracefull shutdown
	close(s.stop)
}

func (s *Server) notifyAll(notify func(Notifiee)) {
	var wg sync.WaitGroup

	s.notifs.RLock()
	wg.Add(len(s.notifs.m))
	for f := range s.notifs.m {
		go func(f Notifiee) {
			defer wg.Done()
			notify(f)
		}(f)
	}

	wg.Wait()
	s.notifs.RUnlock()
}

func (s *Server) Notify(f Notifiee) {
	s.notifs.Lock()
	s.notifs.m[f] = struct{}{}
	s.notifs.Unlock()
}

func (s *Server) StopNotify(f Notifiee) {
	s.notifs.Lock()
	delete(s.notifs.m, f)
	s.notifs.Unlock()
}

func garbageCollectBus(bus *types.Bus, interval time.Duration, expiry time.Duration) {
	ticker := time.NewTicker(interval)
	select {
	case <-ticker.C:
		list := bus.SubscriptionList()
		for _, item := range list {
			if bus.Expired(item, expiry) {
				bus.Unsubscribe(item)
			}
		}
		break
	}
}

func proxyHandler(outgoingBus *types.RequestBus, bus *types.Bus, gatewayTimeout time.Duration) func(w http.ResponseWriter, r *http.Request) {

	return func(w http.ResponseWriter, r *http.Request) {

		inletsID := uuid.Formatter(uuid.NewV4(), uuid.FormatHex)

		sub := bus.Subscribe(inletsID)

		defer func() {
			bus.Unsubscribe(inletsID)
		}()

		log.Printf("[%s] proxy %s %s %s", inletsID, r.Host, r.Method, r.URL.String())
		r.Header.Set(transport.InletsHeader, inletsID)

		if r.Body != nil {
			defer r.Body.Close()
		}

		body, _ := ioutil.ReadAll(r.Body)

		qs := ""
		if len(r.URL.RawQuery) > 0 {
			qs = "?" + r.URL.RawQuery
		}

		req, _ := http.NewRequest(r.Method, fmt.Sprintf("http://%s%s%s", r.Host, r.URL.Path, qs),
			bytes.NewReader(body))

		transport.CopyHeaders(req.Header, &r.Header)

		wg := sync.WaitGroup{}
		wg.Add(2)

		go func() {
			log.Printf("[%s] waiting for response", inletsID)

			select {
			case res := <-sub.Data:

				if res != nil && res.Body != nil {
					innerBody, _ := ioutil.ReadAll(res.Body)

					transport.CopyHeaders(w.Header(), &res.Header)
					w.WriteHeader(res.StatusCode)
					w.Write(innerBody)
					log.Printf("[%s] wrote %d bytes", inletsID, len(innerBody))
				} else {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte("Request or body from request was nil, client may have disconnected"))
					log.Printf("Request or body from request was nil, client may have disconnected")
				}

				wg.Done()
				break
			case <-time.After(gatewayTimeout):
				log.Printf("[%s] timeout after %f secs\n", inletsID, gatewayTimeout.Seconds())

				w.WriteHeader(http.StatusGatewayTimeout)
				wg.Done()
				break
			}
		}()

		go func() {
			outgoingBus.Send(req)
			// outgoing <- req
			wg.Done()
		}()

		wg.Wait()
	}
}

func (s *Server) serveWs(outgoingBus *types.RequestBus, bus *types.Bus) func(w http.ResponseWriter, r *http.Request) {

	var upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
	}

	return func(w http.ResponseWriter, r *http.Request) {

		guid := r.Header.Get(client.IDHeader)
		subject := r.Header.Get(client.SubjectHeader)

		err := authorized(s.cfg.Token, r)

		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(err.Error()))
		}

		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			if _, ok := err.(websocket.HandshakeError); !ok {
				log.Println(err)
			}
			return
		}

		log.Printf("Connecting websocket on: %s", ws.RemoteAddr())

		count.Add(1)
		guids.Set(guid, time.Now())
		// Connected
		s.notifyAll(func(f Notifiee) {
			f.Connected(guid, subject)
		})

		connectionDone := make(chan struct{})

		go func() {
			defer close(connectionDone)
			for {
				msgType, message, err := ws.ReadMessage()
				if err != nil {
					log.Println("read:", err)
					return
				}

				if msgType == websocket.TextMessage {
					log.Println("TextMessage: ", message)
				} else if msgType == websocket.BinaryMessage {

					reader := bytes.NewReader(message)
					scanner := bufio.NewReader(reader)
					res, _ := http.ReadResponse(scanner, nil)

					if id := res.Header.Get(transport.InletsHeader); len(id) > 0 {
						bus.Send(id, res)
					}
				}
			}
		}()

		sub := outgoingBus.Subscribe(ws.LocalAddr().String())

		go func() {
			defer close(connectionDone)
			for {

				log.Printf("wait for request")
				select {
				case outboundRequest := <-sub.Data:
					log.Printf("[%s] request written to websocket", outboundRequest.Header.Get(transport.InletsHeader))

					buf := new(bytes.Buffer)

					outboundRequest.Write(buf)

					ws.WriteMessage(websocket.BinaryMessage, buf.Bytes())
				}
			}

		}()

		<-connectionDone
		count.Add(-1)
		guids.Delete(guid)

		// Disconnected
		s.notifyAll(func(f Notifiee) {
			f.Disconnected(guid)
		})
	}
}

func authorized(token string, r *http.Request) error {

	auth := r.Header.Get("Authorization")
	valid := false
	if len(token) == 0 {
		valid = true
	} else {
		prefix := "Bearer "
		if strings.HasPrefix(auth, prefix); len(auth) > len(prefix) && auth[len(prefix):] == token {
			valid = true
		}
	}

	if !valid {
		return fmt.Errorf("send token in header Authorization: Bearer <token>")
	}

	return nil
}
