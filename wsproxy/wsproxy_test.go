package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

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
	wsServer := httptest.NewServer(getWsHandler(server.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	wsClient, _, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	if wsClient != nil {
		defer wsClient.Close()
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
	wsServer := httptest.NewServer(getWsHandler(server.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	wsClient, _, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	if wsClient != nil {
		defer wsClient.Close()
	}
	got := err.Error()
	want := "websocket: bad handshake"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}
