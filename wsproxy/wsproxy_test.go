package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func waitForPort(host string, timeout time.Duration) {
	conn, err := net.DialTimeout("tcp", host, timeout)
	if err == nil {
		conn.Close()
	}
}

// TestConnect tries to connect with a websocket and checks
// that a websocket connection is made when "ok" is returned.
func TestConnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Method + " " + r.RequestURI
		want := "GET /test"
		if got != want {
			t.Errorf("got %q, wanted %q", got, want)
		}
		w.Write([]byte("ok"))
	}))
	defer server.Close()
	go wsListener(":4000", server.URL+"/")
	waitForPort(":4000", 1*time.Second)
	ws, _, err := websocket.DefaultDialer.Dial("ws://localhost:4000/test", nil)
	if ws != nil {
		defer ws.Close()
	}
	got := err
	want := error(nil)
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestCannotConnect tries to connect with a websocket and checks
// that a websocket connection is failing when "ko" is returned.
func TestCannotConnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ko"))
	}))
	defer server.Close()
	go wsListener(":4000", server.URL+"/")
	waitForPort(":4000", 1*time.Second)
	ws, _, err := websocket.DefaultDialer.Dial("ws://localhost:4000/test", nil)
	if ws != nil {
		defer ws.Close()
	}
	got := err.Error()
	want := "websocket: bad handshake"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}
