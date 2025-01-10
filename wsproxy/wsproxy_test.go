package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestConnectAccepted connects with a websocket and checks
// that a websocket connection is made when "ok" is returned.
func TestConnectAccepted(t *testing.T) {
	// start api server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Method + " " + r.RequestURI
		want := "GET /test"
		if got != want {
			t.Errorf("got %q, wanted %q", got, want)
		}
		w.Write([]byte("ok"))
	}))
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	wsClient, response, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	if wsClient != nil {
		defer wsClient.Close()
	}
	if err != nil {
		t.Errorf("error connecting ws client: %s", err.Error())
	}
	// close ws connection
	err = wsClient.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "done"))
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	got := fmt.Sprintf("%d", response.StatusCode)
	want := "101"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestConnectRejected connects with a websocket and checks
// that a websocket connection is failing when "ko" is returned.
func TestConnectRejected(t *testing.T) {
	// start api server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ko"))
	}))
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	wsClient, response, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	if wsClient != nil {
		defer wsClient.Close()
	}
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	got := fmt.Sprintf("%d: %s", response.StatusCode, errorMessage)
	want := "403: websocket: bad handshake"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestConnectFailed connects with a websocket and checks
// that a 502 is returned when the server is not available.
func TestConnectFailed(t *testing.T) {
	// start api server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte("internal server error"))
	}))
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	wsClient, response, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	if wsClient != nil {
		defer wsClient.Close()
	}
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	got := fmt.Sprintf("%d: %s", response.StatusCode, errorMessage)
	want := "502: websocket: bad handshake"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestIncomingMessage connects with a websocket and sends
// and receives a message in text format over that websocket connection
func TestIncomingMessage(t *testing.T) {
	// start api server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write([]byte("ok"))
			return
		}
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("error reading body: %q", err.Error())
		}
		got := r.Method + " " + r.RequestURI + " " + string(bodyBytes)
		want := "POST /test request_message"
		if got != want {
			t.Errorf("got %q, wanted %q", got, want)
		}
		w.Write([]byte("response_message"))
	}))
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	wsClient, _, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	if wsClient != nil {
		defer wsClient.Close()
	}
	if err != nil {
		t.Errorf("error connecting ws client: %s", err.Error())
	}
	// send ws message
	wsClient.WriteMessage(websocket.TextMessage, []byte("request_message"))
	// receive ws message
	messageType, messageBytes, err := wsClient.ReadMessage()
	if err != nil {
		t.Errorf("error reading from ws client: %s", err.Error())
	}
	// close ws connection
	err = wsClient.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "done"))
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	got := fmt.Sprintf("%d %s", messageType, string(messageBytes))
	want := "1 response_message" // 1 = text message
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestOutgoingMessage connects with a websocket and sends
// and receives a message in text format over that websocket connection
func TestOutgoingMessage(t *testing.T) {
	// start api server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	wsClient, _, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	if wsClient != nil {
		defer wsClient.Close()
	}
	if err != nil {
		t.Errorf("error connecting ws client: %s", err.Error())
	}
	// make post request
	c := &http.Client{}
	c.Post(wsServer.URL+"/test", "plain/text", strings.NewReader("server_message"))
	// receive ws message
	messageType, messageBytes, err := wsClient.ReadMessage()
	if err != nil {
		t.Errorf("error reading from ws client: %s", err.Error())
	}
	// close ws connection
	err = wsClient.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""))
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	got := fmt.Sprintf("%d %s", messageType, string(messageBytes))
	want := "1 server_message" // 1 = text message
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestDisconnectReason disconnects a websocket and checks
// that the reason is received by the server.
func TestDisconnectReason(t *testing.T) {
	// start api server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write([]byte("ok"))
			return
		}
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("error reading body: %q", err.Error())
		}
		got := r.Method + " " + r.RequestURI + " " + string(bodyBytes)
		want := "DELETE /test disconnect"
		if got != want {
			t.Errorf("got %q, wanted %q", got, want)
		}
		w.Write([]byte("ok"))
	}))
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	wsClient, _, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	if err != nil {
		t.Errorf("error connecting ws client: %s", err.Error())
	}
	// close ws connection
	err = wsClient.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "disconnect"))
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	wsClient.ReadMessage()
	wsClient.Close()
}

// TestDisconnectUnexpected disconnects a websocket unexpected and
// checks that the cause is received by the server.
func TestDisconnectUnexpected(t *testing.T) {
	// start api server
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Write([]byte("ok"))
			return
		}
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("error reading body: %q", err.Error())
		}
		got := r.Method + " " + r.RequestURI + " " + string(bodyBytes)
		want := "DELETE /test EOF"
		if got != want {
			t.Errorf("got %q, wanted %q", got, want)
		}
		w.Write([]byte("ok"))
	}))
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	wsClient, _, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	if err != nil {
		t.Errorf("error connecting ws client: %s", err.Error())
	}
	// close ws connection
	wsClient.Close()
	wsClient.SetReadDeadline(time.UnixMilli(1))
	time.Sleep(1 * time.Millisecond)
	wsClient.ReadMessage()
}
