package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

func newAutoReloader() *autoReloader {
	return &autoReloader{
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool { return true },
		},
	}
}

type autoReloader struct {
	// addr     string
	upgrader websocket.Upgrader

	rwmu  sync.RWMutex
	clist []*websocket.Conn

	pid int
}

func (ar *autoReloader) setPid(pid int) {
	ar.pid = pid
	// send out to browser on a slight delay
	go func() {
		time.Sleep(time.Millisecond * 200)
		ar.push([]byte(fmt.Sprintf(`{"type":"exec","pid":%d}`, pid)))
	}()
}

func (ar *autoReloader) push(jsonMessage []byte) {
	if *flagV {
		log.Printf("autoReloader pushing message: %s", jsonMessage)
	}

	ar.rwmu.RLock()
	clist := make([]*websocket.Conn, len(ar.clist))
	copy(clist, ar.clist)
	ar.rwmu.RUnlock()

	rawmsg := json.RawMessage(jsonMessage)
	for _, c := range clist {
		c := c
		go func() {
			err := c.WriteJSON(rawmsg)
			if err != nil {
				if *flagV {
					log.Printf("Error sending message to (%v): %v", c, err)
				}
			}
		}()
	}

}

func (ar *autoReloader) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/listen" {
		ar.serveWS(w, r)
		return
	}
	if r.URL.Path == "/auto-reload.js" {
		ar.serveJS(w, r)
		return
	}
	http.NotFound(w, r)
}

func (ar *autoReloader) serveJS(w http.ResponseWriter, r *http.Request) {

	w.Header().Set("Content-Type", "text/javascript")
	fmt.Fprint(w, `

(function() {

	var pid = 0;

	console.log("vgrun auto-reload.js starting...");

	var connect;
	connect = function() {

		var sock = new WebSocket("ws://`+r.Host+`/listen");

		sock.onmessage = function(event) {
			//console.log("auto-reload received message:", event);
			var data = JSON.parse(event.data);
			if (!pid) { // first value for pid
				pid = data.pid;
				return;
			}
			if (pid == data.pid) { // pid is is the same as last time
				return;
			}
			if (!data.pid) { // should not happen but could in some edge cases where auto-reload server is up but Go process is not
				return;
			}
			// must be different pid
			pid = data.pid;
			console.log("auto-reload reloading for pid ", pid)
			window.location.reload();
		}

		sock.onclose = function(e) {
			console.log('auto-reload socket closed, reconnecting in 2 seconds, reason:', e.reason);
			setTimeout(function() {
				connect();
			}, 2000);
		};
		
		sock.onerror = function(err) {
			console.log('auto-reload socket error, closing, message:', err.message);
			sock.close();
		};
	  
	}

	connect();

})()

`)

}

func (ar *autoReloader) serveWS(w http.ResponseWriter, r *http.Request) {

	c, err := ar.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Print("upgrade:", err)
		return
	}
	defer c.Close()

	ar.rwmu.Lock()
	ar.clist = append(ar.clist, c)
	ar.rwmu.Unlock()

	defer func() {
		ar.rwmu.Lock()
		for i, cl := range ar.clist {
			if cl == c { // remove clist[i]
				s := ar.clist
				s[len(s)-1], s[i] = s[i], s[len(s)-1]
				s = s[:len(s)-1]
				ar.clist = s
				break
			}
		}
		ar.rwmu.Unlock()
	}()

	// upon first connect we send them the current pid

	rawmsg := json.RawMessage(fmt.Sprintf(`{"type":"last_exec","pid":%d}`, ar.pid))
	err = c.WriteJSON(rawmsg)
	if err != nil {
		log.Println("WriteJSON error:", err)
		return
	}

	// just read messages indefinitely until error (client disconnects)
	for {

		mt, message, err := c.ReadMessage()
		if err != nil {
			if *flagV {
				log.Println("read error:", err)
			}
			break
		}
		_, _ = mt, message

	}

}
