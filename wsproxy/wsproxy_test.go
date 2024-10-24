package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestHelloName calls greetings.Hello with a name, checking
// for a valid return value.
func TestConnect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	go wsListener(":4000", server.URL+"/")
	time.Sleep(1 * time.Second)
	// Connect to the server
	ws, _, err := websocket.DefaultDialer.Dial("ws://localhost:4000/test", nil)
	if err != nil {
		t.Fatalf("%v", err)
	}
	defer ws.Close()
}
