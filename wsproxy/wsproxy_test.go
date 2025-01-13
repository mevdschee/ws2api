package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/lxzan/gws"
)

// startLockStepTestWebServer creates a scriptable webserver that has request and response channel to lock-step execution
func startLockStepTestWebServer(t *testing.T) (apiServer *httptest.Server, requests chan string, responses chan string) {
	requests = make(chan string, 1)
	responses = make(chan string, 1)
	apiServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
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
	//t.Log(string(bodyBytes))
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
	wsClient, response, err := gws.NewClient(nil, &gws.ClientOption{Addr: wsUrl + "/test"})
	request := <-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	// close ws connection
	responses <- "200 ok"
	err = wsClient.WriteClose(1000, []byte("done"))
	<-requests
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "requests_started")
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
	_, response, err := gws.NewClient(nil, &gws.ClientOption{Addr: wsUrl + "/test"})
	request := <-requests
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "requests_started")
	// compare results
	got := fmt.Sprintf("%d %d %s %s", counter1, response.StatusCode, errorMessage, request)
	want := "1 403 handshake error GET /test"
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
	_, response, err := gws.NewClient(nil, &gws.ClientOption{Addr: wsUrl + "/test"})
	request := <-requests
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "requests_started")
	// compare results
	got := fmt.Sprintf("%d %d %s %s", counter1, response.StatusCode, errorMessage, request)
	want := "1 502 handshake error GET /test"
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
	wsClient, _, err := gws.NewClient(nil, &gws.ClientOption{Addr: wsUrl + "/test"})
	<-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	// send ws message
	responses <- "200 response_message"
	wsClient.WriteMessage(gws.OpcodeText, []byte("request_message"))
	request := <-requests
	// receive ws message
	messageBytes := make([]byte, 1024) // 1k buffer
	messageLength, err := wsClient.NetConn().Read(messageBytes)
	if err != nil {
		t.Errorf("error reading from ws client: %s", err.Error())
	}
	// close ws connection
	responses <- "200 ok"
	err = wsClient.WriteClose(1000, []byte("done"))
	<-requests
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "requests_started")
	// compare results
	got := fmt.Sprintf("%d %s %s", counter1, string(messageBytes[:messageLength]), request)
	want := "3 \x81\x10response_message POST /test request_message" // \x81 = text message, \x10 = length
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
	wsClient, _, err := gws.NewClient(nil, &gws.ClientOption{Addr: wsUrl + "/test"})
	<-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	// make post request
	c := &http.Client{}
	c.Post(wsServer.URL+"/test", "plain/text", strings.NewReader("server_message"))
	// receive ws message
	messageBytes := make([]byte, 1024) // 1k buffer
	messageLength, err := wsClient.NetConn().Read(messageBytes)
	if err != nil {
		t.Errorf("error reading from ws client: %s", err.Error())
	}
	// close ws connection
	responses <- "200 ok"
	err = wsClient.WriteClose(1000, []byte(""))
	<-requests
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "requests_started")
	// compare results
	got := fmt.Sprintf("%d %s", counter1, string(messageBytes[:messageLength]))
	want := "2 \x81\x0eserver_message" // \x81 = text message, \x0e = length
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
	wsClient, _, err := gws.NewClient(nil, &gws.ClientOption{Addr: wsUrl + "/test"})
	<-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	// close ws connection
	responses <- "200 ok"
	err = wsClient.WriteClose(1000, []byte("disconnect"))
	request := <-requests
	if err != nil {
		t.Errorf("error closing ws from client: %s", err.Error())
	}
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "requests_started")
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
	wsClient, _, err := gws.NewClient(nil, &gws.ClientOption{Addr: wsUrl + "/test"})
	<-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	// close ws connection
	responses <- "200 ok"
	wsClient.NetConn().Close()
	request := <-requests
	// read number of request sent
	counter1 := getCounterValueFromStatisticsUrl(t, wsServer.URL, "requests_started")
	// compare results
	got := fmt.Sprintf("%d %s", counter1, request)
	want := "2 DELETE /test EOF"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}
