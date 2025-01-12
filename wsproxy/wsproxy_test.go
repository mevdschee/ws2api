package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startLockStepTestWebServer creates a scriptable webserver that has request and response channel to lock-step execution
func startLockStepTestWebServer(t *testing.T) (apiServer *httptest.Server, requests chan string, responses chan string) {
	requests = make(chan string, 1)
	responses = make(chan string, 1)
	apiServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("error reading body: %q", err.Error())
		}
		request := strings.Trim(r.Method+" "+r.RequestURI+" "+string(bodyBytes), " ")
		if len(requests) != 0 {
			t.Fatalf("unexpected request: %q", request)
		}
		requests <- request
		if len(responses) != 1 {
			t.Fatalf("no response for request: %q", request)
		}
		parts := strings.SplitN(<-responses, " ", 2)
		status, err := strconv.Atoi(parts[0])
		if err != nil {
			t.Errorf("error parsing reponse: %q", err.Error())
		}
		w.WriteHeader(status)
		if len(parts) > 1 {
			w.Write([]byte(parts[1]))
		}
	}))
	return
}

// getCounterFromStatistics gets a counter from a statistics url (in OpenMetrics format)
func getCounterValueFromStatisticsUrl(t *testing.T, url string, counterName string) int64 {
	c := &http.Client{}
	response, err := c.Get(url)
	if err != nil {
		t.Errorf("could not get statistics: %q", err.Error())
	}
	bodyBytes, err := io.ReadAll(response.Body)
	if err != nil {
		t.Errorf("error reading body: %q", err.Error())
	}
	lines := strings.Split(string(bodyBytes), "\n")
	for _, line := range lines {
		line = strings.Trim(line, " ")
		if len(line) < 1 || line[0:1] == "#" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) > 1 {
			if parts[0] == counterName {
				counter, err := strconv.ParseInt(parts[1], 10, 64)
				if err != nil {
					t.Errorf("error parsing int: %q", err.Error())
				}
				return counter
			}
		}
	}
	return 0
}

// TestConnectAccepted connects with a websocket and checks
// that a websocket connection is made when "ok" is returned.
func TestConnectAccepted(t *testing.T) {
	// start api server
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	responses <- "200 ok"
	wsClient, response, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	request := <-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	defer wsClient.Close()
	// close ws connection
	responses <- "200 ok"
	err = wsClient.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "done"))
	<-requests
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	wsClient.ReadMessage()
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "request_count")
	// compare results
	got := fmt.Sprintf("%d %d %s", counter1, response.StatusCode, request)
	want := "2 101 GET /test"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestConnectRejected connects with a websocket and checks
// that a websocket connection is failing when "ko" is returned.
func TestConnectRejected(t *testing.T) {
	// start api server
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	responses <- "200 ko"
	wsClient, response, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	request := <-requests
	if err == nil {
		defer wsClient.Close()
	}
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "request_count")
	// compare results
	got := fmt.Sprintf("%d %d %s %s", counter1, response.StatusCode, errorMessage, request)
	want := "1 403 websocket: bad handshake GET /test"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestConnectFailed connects with a websocket and checks
// that a 502 is returned when the server is not available.
func TestConnectFailed(t *testing.T) {
	// start api server
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	responses <- "503 service unavailable"
	wsClient, response, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	request := <-requests
	if err == nil {
		defer wsClient.Close()
	}
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "request_count")
	// compare results
	got := fmt.Sprintf("%d %d %s %s", counter1, response.StatusCode, errorMessage, request)
	want := "1 502 websocket: bad handshake GET /test"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestIncomingMessage connects with a websocket and sends
// and receives a message in text format over that websocket connection
func TestIncomingMessage(t *testing.T) {
	// start api server
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	responses <- "200 ok"
	wsClient, _, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	<-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	defer wsClient.Close()
	// send ws message
	responses <- "200 response_message"
	wsClient.WriteMessage(websocket.TextMessage, []byte("request_message"))
	request := <-requests
	// receive ws message
	messageType, messageBytes, err := wsClient.ReadMessage()
	if err != nil {
		t.Errorf("error reading from ws client: %s", err.Error())
	}
	// close ws connection
	responses <- "200 ok"
	err = wsClient.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "done"))
	<-requests
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	wsClient.ReadMessage()
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "request_count")
	// compare results
	got := fmt.Sprintf("%d %d %s %s", counter1, messageType, string(messageBytes), request)
	want := "3 1 response_message POST /test request_message" // 1 = text message
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestOutgoingMessage connects with a websocket and sends
// and receives a message in text format over that websocket connection
func TestOutgoingMessage(t *testing.T) {
	// start api server
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	responses <- "200 ok"
	wsClient, _, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	<-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	defer wsClient.Close()
	// make post request
	c := &http.Client{}
	c.Post(wsServer.URL+"/test", "plain/text", strings.NewReader("server_message"))
	// receive ws message
	messageType, messageBytes, err := wsClient.ReadMessage()
	if err != nil {
		t.Errorf("error reading from ws client: %s", err.Error())
	}
	// close ws connection
	responses <- "200 ok"
	err = wsClient.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, ""))
	<-requests
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	wsClient.ReadMessage()
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "request_count")
	// compare results
	got := fmt.Sprintf("%d %d %s", counter1, messageType, string(messageBytes))
	want := "2 1 server_message" // 1 = text message
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestDisconnectReason disconnects a websocket and checks
// that the reason is received by the server.
func TestDisconnectReason(t *testing.T) {
	// start api server
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	responses <- "200 ok"
	wsClient, _, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	<-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	defer wsClient.Close()
	// close ws connection
	responses <- "200 ok"
	err = wsClient.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(1000, "disconnect"))
	request := <-requests
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	wsClient.ReadMessage()
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "request_count")
	// compare results
	got := fmt.Sprintf("%d %s", counter1, request)
	want := "2 DELETE /test disconnect"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestDisconnectUnexpected disconnects a websocket unexpected and
// checks that the cause is received by the server.
func TestDisconnectUnexpected(t *testing.T) {
	// start api server
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	// start ws server
	wsServer := httptest.NewServer(getWsHandler(apiServer.URL + "/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)
	// connect to ws server
	responses <- "200 ok"
	wsClient, _, err := websocket.DefaultDialer.Dial(wsUrl+"/test", nil)
	<-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	// close ws connection
	responses <- "200 ok"
	wsClient.Close()
	wsClient.SetReadDeadline(time.UnixMilli(1))
	time.Sleep(1 * time.Millisecond)
	wsClient.ReadMessage()
	request := <-requests
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "request_count")
	// compare results
	got := fmt.Sprintf("%d %s", counter1, request)
	want := "2 DELETE /test EOF"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}
