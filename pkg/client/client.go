package client

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"fhyx/inlets/pkg/transport"

	"github.com/gorilla/websocket"
)

const (
	IDHeader      = "x-client-id"
	SubjectHeader = "x-client-subject"
)

var httpClient *http.Client

func New(cfg *Configuration) *Client {
	return &Client{
		cfg:  cfg,
		stop: make(chan struct{}),
	}
}

// Client for inlets
type Client struct {
	cfg *Configuration

	stop chan struct{}
}

// Connect connect and serve traffic through websocket
func (c *Client) Serve() {

	httpClient = http.DefaultClient
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	remote := c.cfg.Remote
	if !strings.HasPrefix(remote, "wss") {
		remote = "wss://" + remote
	}

	remoteURL, urlErr := url.Parse(remote)
	if urlErr != nil {
		log.Printf("bad remote URL %s ERR:%s", remoteURL, urlErr)
		return
	}

	u := url.URL{Scheme: remoteURL.Scheme, Host: remoteURL.Host, Path: "/tunnel"}

	log.Printf("connecting to %s with ping=%s", u.String(), c.cfg.PingWaitDuration.String())
	dialer := &websocket.Dialer{
		ReadBufferSize:   2048,
		WriteBufferSize:  2048,
		HandshakeTimeout: 45 * time.Second,
		TLSClientConfig:  c.cfg.TLSConfig,
	}
	wsc, _, err := dialer.Dial(u.String(), http.Header{
		IDHeader:        []string{c.cfg.ID},
		SubjectHeader:   []string{c.cfg.Subject},
		"Authorization": []string{"Bearer " + c.cfg.Token},
	})
	if err != nil {
		log.Printf("dial server %s ERR:%s", u.String(), err)
		return
	}
	ws := transport.NewWebsocketConn(wsc, c.cfg.PingWaitDuration)

	if err != nil {
		log.Printf("new websocket conn ERR:%s", err)
		return
	}

	log.Printf("Connected to websocket: %s", ws.LocalAddr())

	defer wsc.Close()

	// Send pings
	tickerDone := make(chan bool)

	go func() {
		log.Printf("Writing pings")

		ticker := time.NewTicker((c.cfg.PingWaitDuration * 9) / 10) // send on a period which is around 9/10ths of original value
		for {
			select {
			case <-ticker.C:
				if err := ws.Ping(); err != nil {
					close(tickerDone)
				}
				break
			case <-tickerDone:
				log.Printf("tickerDone, no more pings will be sent from client\n")
				return
			}
		}
	}()

	// Work with websocket
	done := make(chan struct{})

	go func() {
		defer close(done)

		for {
			messageType, message, err := ws.ReadMessage()
			fmt.Printf("Read a message from websocket.\n")
			if err != nil {
				fmt.Printf("Read error: %s.\n", err)
				return
			}

			switch messageType {
			case websocket.TextMessage:
				log.Printf("TextMessage: %s\n", message)

				break
			case websocket.BinaryMessage:
				// proxyToUpstream

				buf := bytes.NewBuffer(message)
				bufReader := bufio.NewReader(buf)
				req, readReqErr := http.ReadRequest(bufReader)
				if readReqErr != nil {
					log.Println(readReqErr)
					return
				}

				inletsID := req.Header.Get(transport.InletsHeader)

				log.Printf("[%s] %s", inletsID, req.RequestURI)

				body, _ := ioutil.ReadAll(req.Body)

				proxyHost := ""
				if val, ok := c.cfg.UpstreamMap[req.Host]; ok {
					proxyHost = val
				} else if val, ok := c.cfg.UpstreamMap[""]; ok {
					proxyHost = val
				}

				targetURL, paseHostErr := url.Parse(proxyHost)
				if paseHostErr != nil {
					log.Printf("[%s] pase upstream target host err: %s", inletsID, paseHostErr)
					return
				}

				targetURL.Path = path.Join(targetURL.Path, req.URL.Path)
				targetURL.RawQuery = req.URL.RawQuery

				log.Printf("[%s] proxy => %s", inletsID, targetURL.String())

				newReq, newReqErr := http.NewRequest(req.Method, targetURL.String(), bytes.NewReader(body))
				if newReqErr != nil {
					log.Printf("[%s] newReqErr: %s", inletsID, newReqErr.Error())
					return
				}

				transport.CopyHeaders(newReq.Header, &req.Header)

				res, resErr := httpClient.Do(newReq)

				if resErr != nil {
					log.Printf("[%s] Upstream tunnel err: %s", inletsID, resErr.Error())

					errRes := http.Response{
						StatusCode: http.StatusBadGateway,
						Body:       ioutil.NopCloser(strings.NewReader(resErr.Error())),
						Header:     http.Header{},
					}

					errRes.Header.Set(transport.InletsHeader, inletsID)
					buf2 := new(bytes.Buffer)
					errRes.Write(buf2)
					if errRes.Body != nil {
						errRes.Body.Close()
					}

					ws.WriteMessage(websocket.BinaryMessage, buf2.Bytes())

				} else {
					log.Printf("[%s] tunnel res.Status => %s", inletsID, res.Status)

					buf2 := new(bytes.Buffer)
					res.Header.Set(transport.InletsHeader, inletsID)

					res.Write(buf2)
					if res.Body != nil {
						res.Body.Close()
					}

					log.Printf("[%s] %d bytes", inletsID, buf2.Len())

					ws.WriteMessage(websocket.BinaryMessage, buf2.Bytes())
				}
			}

		}
	}()

	<-done
}

func (c *Client) Stop() {
	//TODO gracefull shutdown
	close(c.stop)
}
