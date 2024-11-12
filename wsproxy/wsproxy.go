package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"
	"time"

	"github.com/avast/retry-go"
	"github.com/gorilla/websocket"
	"github.com/mevdschee/php-observability/metrics"
	"github.com/mevdschee/php-wamp-observer/tracking"
)

func init() {
	runtime.GOMAXPROCS(8)
}

func fetchData(c *http.Client, url string, body string) (string, error) {
	var r *http.Response
	var err error
	if len(body) == 0 {
		r, err = c.Get(url)
	} else {
		r, err = c.Post(url, "plain/text", strings.NewReader(body))
	}
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	responseBytes, err := io.ReadAll(r.Body)
	responseString := string(responseBytes)
	if err != nil {
		return responseString, err
	}
	if r.StatusCode != 200 {
		return responseString, fmt.Errorf("proxy returned: %s", r.Status)
	}
	return responseString, nil
}

// fetchDataWithRetries is your wrapped retrieval.
// It works with a static configuration for the retries,
// but obviously, you can generalize this function further.
func fetchDataWithRetries(c *http.Client, url string, body string) (message string, err error) {
	retry.Do(
		// The actual function that does "stuff"
		func() error {
			message, err = fetchData(c, url, body)
			return err
		},
		// A function to decide whether you actually want to
		// retry or not. In this case, it would make sense
		// to actually stop retrying, since the host does not exist.
		// Return true if you want to retry, false if not.
		retry.RetryIf(
			func(error) bool {
				log.Printf("Retrieving data: %s", err)
				log.Printf("Deciding whether to retry")
				return true
			}),
		retry.OnRetry(func(try uint, orig error) {
			log.Printf("Retrying to fetch data. Try: %d", try+2)

		}),
		retry.Attempts(3),
		// Basically, we are setting up a delay
		// which randoms between 2 and 4 seconds.
		retry.Delay(3*time.Second),
		retry.MaxJitter(1*time.Second),
	)
	return
}

var stats = metrics.New()
var track = tracking.New(stats, 30*time.Second)

func main() {
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to file")
	memprofile := flag.String("memprofile", "", "write mem profile to file")
	listenAddress := flag.String("listen", ":4000", "address to listen for high frequent events over TCP")
	binaryAddress := flag.String("binary", ":9999", "address to listen for Gob metric scraper over HTTP")
	metricsAddress := flag.String("metrics", ":8080", "address to listen for Prometheus metric scraper over HTTP")
	serverUrl := flag.String("url", "http://localhost:5000/", "url of the API server to relay websocket messages to")
	flag.Parse()
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}
	go serve(*memprofile, *metricsAddress)
	go serveGob(*binaryAddress)
	log.Printf("listening on %s proxying to %s", *listenAddress, *serverUrl)
	log.Fatal(http.ListenAndServe(*listenAddress, getWsHandler(*serverUrl)))
}

func getWsHandler(serverUrl string) http.Handler {
	return webSocketHandler{
		mutex:     &sync.Mutex{},
		upgrader:  websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		sockets:   map[string]*webSocketConnection{},
		serverUrl: serverUrl,
	}
}

func serve(memprofile, metricsAddress string) {
	err := http.ListenAndServe(metricsAddress, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		stats.Write(writer)
		if memprofile != "" {
			f, err := os.Create(memprofile)
			if err != nil {
				log.Fatal(err)
			}
			pprof.WriteHeapProfile(f)
			f.Close()
		}
	}))
	log.Fatal(err)
}

func serveGob(metricsAddress string) {
	err := http.ListenAndServe(metricsAddress, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		stats.WriteGob(writer)
	}))
	log.Fatal(err)
}

type webSocketHandler struct {
	upgrader  websocket.Upgrader
	mutex     *sync.Mutex
	sockets   map[string]*webSocketConnection
	serverUrl string
}

func (wsh webSocketHandler) storeConnection(address string, connection *websocket.Conn, client *http.Client) *webSocketConnection {
	wsh.mutex.Lock()
	defer wsh.mutex.Unlock()
	s := &webSocketConnection{
		readLock:   &sync.Mutex{},
		writeLock:  &sync.Mutex{},
		connection: connection,
		client:     client,
	}
	wsh.sockets[address] = s
	return s
}

func (wsh webSocketHandler) retrieveConnection(address string) *webSocketConnection {
	wsh.mutex.Lock()
	defer wsh.mutex.Unlock()
	return wsh.sockets[address]
}

func (wsh webSocketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	address := strings.Split(r.URL.Path, "/")[1]
	if len(address) == 0 {
		w.WriteHeader(400)
		w.Write([]byte("invalid url, use /address"))
		return
	}
	if r.Method == http.MethodPost {
		s := wsh.retrieveConnection(address)
		if s == nil {
			w.WriteHeader(404)
			w.Write([]byte("could not find address: " + address))
			return
		}
		requestBody, err := io.ReadAll(r.Body)
		r.Body.Close()
		message := string(requestBody)
		if err != nil {
			w.WriteHeader(500)
			w.Write([]byte("could not read body"))
			return
		}
		err = s.handleOutgoingMessage(message)
		if err != nil {
			w.WriteHeader(500)
			w.Write([]byte(err.Error()))
			return
		}
		return
	}
	if r.Header.Get("Upgrade") != "websocket" {
		w.WriteHeader(500)
		w.Write([]byte("no upgrade requested"))
		return
	}
	client := &http.Client{}
	responseBytes, err := fetchData(client, wsh.serverUrl+address, "")
	if err != nil {
		w.WriteHeader(502)
		w.Write([]byte("error when proxying connect"))
		return
	}
	if responseBytes != "ok" {
		w.WriteHeader(403)
		w.Write([]byte("not allowed to connect"))
		return
	}
	c, err := wsh.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("error when upgrading connection to websocket: %s", err)
		return
	}
	defer c.Close()
	s := wsh.storeConnection(address, c, client)
	stats.Inc("wsproxy_connection", "event", "start", 1)
	for {
		// receive message
		message, err := s.readString()
		if err != nil {
			log.Printf("error %s", err)
			break
		}
		stats.Inc("wsproxy_message", "event", "start", 1)
		//log.Printf("Receive message %s", message)
		start := time.Now()
		err = s.handleIncomingMessage(address, message, wsh.serverUrl)
		stats.Add("wsproxy_message", "address", address, time.Since(start).Seconds())
		stats.Inc("wsproxy_message", "event", "finish", 1)
		if err != nil {
			log.Printf("error %s", err)
			break
		}
	}
	stats.Inc("wsproxy_connection", "event", "finish", 1)
}

type webSocketConnection struct {
	readLock   *sync.Mutex
	writeLock  *sync.Mutex
	connection *websocket.Conn
	client     *http.Client
}

func (s webSocketConnection) readString() (string, error) {
	s.readLock.Lock()
	defer s.readLock.Unlock()
	mt, msg, err := s.connection.ReadMessage()
	if err != nil {
		return "", err
	}
	if mt == websocket.BinaryMessage {
		return "", fmt.Errorf("binary messages not supported")
	}
	return string(msg), nil
}

func (s webSocketConnection) writeString(message string) error {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	return s.connection.WriteMessage(websocket.TextMessage, []byte(message))
}

func (s webSocketConnection) handleIncomingMessage(address string, message string, url string) error {
	// track message
	if message[0] == '[' && message[1] == '2' {
		track.Track("wamp_in", message)
	}
	if message[0] == '[' && message[1] == '3' {
		track.Track("wamp_out", message)
	}
	// handle message
	response, err := fetchDataWithRetries(s.client, url+address, message)
	if err != nil {
		return err
	}
	if len(response) > 0 {
		err = s.writeString(response)
		if err != nil {
			return err
		}
		// track message
		if message[0] == '[' && message[1] == '3' {
			track.Track("wamp_in", response)
		}
	}
	return nil
}

func (s webSocketConnection) handleOutgoingMessage(message string) error {
	// track message
	if message[0] == '[' && message[1] == '2' {
		track.Track("wamp_out", message)
	}
	// handle message
	return s.writeString(message)
}
