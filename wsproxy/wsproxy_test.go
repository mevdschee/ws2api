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

// testHandler builds a proxy handler pointed at a test API server, failing the
// test rather than the request if the configuration is not usable.
func testHandler(t *testing.T, apiURL string) http.Handler {
	t.Helper()
	handler, err := getWsHandler(Config{APIURL: apiURL})
	if err != nil {
		t.Fatalf("could not build the handler: %s", err.Error())
	}
	return handler
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
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
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
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
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
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
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
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
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
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
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
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
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
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
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

// readFrame reads one websocket frame with a deadline, so a test that is going
// to fail does so quickly rather than hanging.
func readFrame(t *testing.T, client *gws.Conn) string {
	t.Helper()
	if err := client.NetConn().SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("could not set a read deadline: %s", err.Error())
	}
	buffer := make([]byte, 1024)
	n, err := client.NetConn().Read(buffer)
	if err != nil {
		return ""
	}
	return string(buffer[:n])
}

// connect opens a websocket to the proxy, answering its authentication call.
func connect(t *testing.T, wsUrl, id string, requests, responses chan string) *gws.Conn {
	t.Helper()
	responses <- "200 ok"
	client, _, err := gws.NewClient(nil, &gws.ClientOption{Addr: wsUrl + "/" + id})
	<-requests
	if err != nil {
		t.Fatalf("error connecting ws client: %s", err.Error())
	}
	return client
}

// TestDuplicateClientIdRefused checks that a second connection claiming a
// ClientId that is already connected is refused, and that refusing it leaves
// the first connection reachable.
//
// Storing over the first connection used to leave it in the map under a key
// pointing at the second, so closing the first would unregister the second and
// a live socket would silently stop receiving pushes.
func TestDuplicateClientIdRefused(t *testing.T) {
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)

	first := connect(t, wsUrl, "test", requests, responses)

	// A second connection with the same ClientId. The handshake succeeds and
	// the proxy then closes it, because the id is taken.
	responses <- "200 ok"
	second, response, err := gws.NewClient(nil, &gws.ClientOption{Addr: wsUrl + "/test"})
	<-requests
	if err != nil {
		t.Fatalf("error connecting the second ws client: %s", err.Error())
	}
	defer second.NetConn().Close()

	denied := getCounterValueFromStatisticsUrl(t, wsServer.URL, "connections_denied")
	active := getCounterValueFromStatisticsUrl(t, wsServer.URL, "active_connections")

	// The first connection must still take a server push.
	c := &http.Client{}
	pushed, err := c.Post(wsServer.URL+"/test", "text/plain", strings.NewReader("still_here"))
	if err != nil {
		t.Fatalf("error posting to the proxy: %s", err.Error())
	}
	defer pushed.Body.Close()
	frame := readFrame(t, first)

	got := fmt.Sprintf("%d %d %d %d %s", response.StatusCode, denied, active, pushed.StatusCode, frame)
	want := "101 1 1 200 \x81\nstill_here" // \x81 = text message, \n = length 10
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestEmptyResponseNotForwarded checks that a handler with nothing to say
// produces no frame at all, rather than an empty one every client has to filter.
func TestEmptyResponseNotForwarded(t *testing.T) {
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)

	client := connect(t, wsUrl, "test", requests, responses)

	// The API accepts the message and answers with an empty body.
	responses <- "200 "
	client.WriteMessage(gws.OpcodeText, []byte("no_reply_wanted"))
	<-requests

	// A push after it, so that the next frame the client reads is this one if
	// and only if no empty frame was written first.
	c := &http.Client{}
	if _, err := c.Post(wsServer.URL+"/test", "text/plain", strings.NewReader("marker")); err != nil {
		t.Fatalf("error posting to the proxy: %s", err.Error())
	}

	got := readFrame(t, client)
	want := "\x81\x06marker"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestFailedRequestNotForwarded checks that the body of a failed API call does
// not reach the client. Forwarding it would put an error page on the wire
// dressed as an ordinary reply.
func TestFailedRequestNotForwarded(t *testing.T) {
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)

	client := connect(t, wsUrl, "test", requests, responses)

	responses <- "500 internal server error"
	client.WriteMessage(gws.OpcodeText, []byte("boom"))
	<-requests

	c := &http.Client{}
	if _, err := c.Post(wsServer.URL+"/test", "text/plain", strings.NewReader("marker")); err != nil {
		t.Fatalf("error posting to the proxy: %s", err.Error())
	}

	failed := getCounterValueFromStatisticsUrl(t, wsServer.URL, "requests_failed")
	got := fmt.Sprintf("%d %s", failed, readFrame(t, client))
	want := "1 \x81\x06marker"
	if got != want {
		t.Errorf("got %q, wanted %q", got, want)
	}
}

// TestPushToUnknownClient checks that a push to a ClientId that is not
// connected answers 404, which is how the API learns a client has gone.
func TestPushToUnknownClient(t *testing.T) {
	apiServer, _, _ := startLockStepTestWebServer(t)
	defer apiServer.Close()
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
	defer wsServer.Close()

	c := &http.Client{}
	response, err := c.Post(wsServer.URL+"/nobody", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("error posting to the proxy: %s", err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Errorf("got %d, wanted %d", response.StatusCode, http.StatusNotFound)
	}
}

// TestOversizedPushRefused checks that a push larger than the cap is refused
// rather than buffered or silently truncated onto the socket.
func TestOversizedPushRefused(t *testing.T) {
	apiServer, requests, responses := startLockStepTestWebServer(t)
	defer apiServer.Close()
	handler, err := getWsHandler(Config{APIURL: apiServer.URL + "/", MaxBodyBytes: 8})
	if err != nil {
		t.Fatalf("could not build the handler: %s", err.Error())
	}
	wsServer := httptest.NewServer(handler)
	defer wsServer.Close()
	wsUrl := strings.Replace(wsServer.URL, "http://", "ws://", 1)

	connect(t, wsUrl, "test", requests, responses)

	c := &http.Client{}
	response, err := c.Post(wsServer.URL+"/test", "text/plain", strings.NewReader("far too many bytes"))
	if err != nil {
		t.Fatalf("error posting to the proxy: %s", err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("got %d, wanted %d", response.StatusCode, http.StatusRequestEntityTooLarge)
	}
}

// TestConfigIsValidated checks that a configuration the proxy cannot use is
// reported at start-up, rather than producing requests that look like the API
// is broken.
func TestConfigIsValidated(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		wantOK bool
	}{
		{"no url", Config{}, false},
		{"no trailing slash", Config{APIURL: "http://api:8080/wsoverhttp"}, false},
		{"not http", Config{APIURL: "api:8080/wsoverhttp/"}, false},
		{"usable", Config{APIURL: "http://api:8080/wsoverhttp/"}, true},
		{"https", Config{APIURL: "https://api/wsoverhttp/"}, true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := getWsHandler(test.config)
			if test.wantOK && err != nil {
				t.Errorf("a usable config was refused: %s", err.Error())
			}
			if !test.wantOK && err == nil {
				t.Errorf("an unusable config was accepted")
			}
		})
	}
}

// TestStatisticsAreScrapable checks that the statistics endpoint carries the
// type metadata a scraper needs. The README promises OpenMetrics; without a
// TYPE line a counter is just a number.
func TestStatisticsAreScrapable(t *testing.T) {
	apiServer, _, _ := startLockStepTestWebServer(t)
	defer apiServer.Close()
	wsServer := httptest.NewServer(testHandler(t, apiServer.URL+"/"))
	defer wsServer.Close()

	c := &http.Client{}
	response, err := c.Get(wsServer.URL)
	if err != nil {
		t.Fatalf("could not get statistics: %s", err.Error())
	}
	defer response.Body.Close()
	bodyBytes, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("error reading body: %s", err.Error())
	}
	body := string(bodyBytes)
	for _, want := range []string{
		"# TYPE requests_started counter",
		"# HELP requests_started ",
		"# TYPE active_connections gauge",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("statistics do not carry %q:\n%s", want, body)
		}
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Errorf("Content-Type = %q", contentType)
	}
}
