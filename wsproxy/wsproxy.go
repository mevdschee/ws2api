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
)

func init() {
	runtime.GOMAXPROCS(8)
}

// fetchDataWithRetries is your wrapped retrieval.
// It works with a static configuration for the retries,
// but obviously, you can generalize this function further.
func fetchDataWithRetries(c *http.Client, url string, body string) (message string, err error) {
	retry.Do(
		// The actual function that does "stuff"
		func() error {
			var r *http.Response
			var err error
			if len(body) == 0 {
				r, err = c.Get(url)
			} else {
				r, err = c.Post(url, "plain/text", strings.NewReader(body))
			}
			if err != nil {
				return err
			}
			defer r.Body.Close()
			var responseBytes []byte
			responseBytes, err = io.ReadAll(r.Body)
			if err != nil {
				return err
			}
			message = string(responseBytes)
			return nil
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
	log.Fatal(http.ListenAndServe(*listenAddress, getWsHandler(*serverUrl)))
}

type webSocketHandler struct {
	upgrader    websocket.Upgrader
	mutex       *sync.Mutex
	connections map[string]*webSocket
	serverUrl   string
}

type webSocket struct {
	readLock   *sync.Mutex
	writeLock  *sync.Mutex
	connection *websocket.Conn
}

func getWsHandler(serverUrl string) http.Handler {
	wsh := webSocketHandler{
		mutex:       &sync.Mutex{},
		upgrader:    websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		connections: map[string]*webSocket{},
		serverUrl:   serverUrl,
	}
	return wsh
}

func serve(memprofile, metricsAddress string) {
	err := http.ListenAndServe(metricsAddress, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		stats.Write(&writer)
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
		stats.WriteGob(&writer)
	}))
	log.Fatal(err)
}

func (wsh webSocketHandler) storeConnection(c *websocket.Conn, address string) *webSocket {
	wsh.mutex.Lock()
	defer wsh.mutex.Unlock()
	s := &webSocket{
		readLock:   &sync.Mutex{},
		writeLock:  &sync.Mutex{},
		connection: c,
	}
	wsh.connections[address] = s
	return s
}

func (wsh webSocketHandler) retrieveConnection(address string) *webSocket {
	wsh.mutex.Lock()
	defer wsh.mutex.Unlock()
	return wsh.connections[address]
}

func (wsh webSocketHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(r.URL.Path, "/")
	address := parts[1]
	if len(parts) != 2 || len(address) == 0 {
		w.WriteHeader(400)
		w.Write([]byte("invalid url, use /address"))
		return
	}
	switch r.Method {
	case http.MethodPost: // post
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
	default: // get
		if r.Header.Get("Upgrade") == "" {
			w.WriteHeader(500)
			w.Write([]byte("no upgrade requested"))
			return
		}
		client := &http.Client{}
		responseBytes, err := fetchDataWithRetries(client, wsh.serverUrl+address, "")
		if err != nil {
			log.Printf("error %s when proxying connect", err)
			return
		}
		if responseBytes != "ok" {
			log.Printf("not allowed to connect: %s", responseBytes)
			return
		}
		c, err := wsh.upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("error %s when upgrading connection to websocket", err)
			return
		}
		defer c.Close()
		s := wsh.storeConnection(c, address)
		stats.Inc("wsproxy_connection", "event", "start", 1)
		for {
			stats.Inc("wsproxy_message", "event", "start", 1)
			// receive message
			message, err := s.readString()
			if err != nil {
				log.Printf("error %s", err)
				break
			}
			//log.Printf("Receive message %s", message)
			start := time.Now()
			err = s.handleIncomingMessage(address, client, message, wsh.serverUrl)
			stats.Add("wsproxy_message", "address", address, time.Since(start).Seconds())
			stats.Inc("wsproxy_message", "event", "finish", 1)
			if err != nil {
				log.Printf("error %s", err)
				break
			}
		}
		stats.Inc("wsproxy_connection", "event", "finish", 1)
	}
}

func (s webSocket) readString() (string, error) {
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

func (s webSocket) writeString(message string) error {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()
	return s.connection.WriteMessage(websocket.TextMessage, []byte(message))
}

func (s *webSocket) handleIncomingMessage(address string, client *http.Client, message string, url string) error {
	// handle message
	responseBytes, err := fetchDataWithRetries(client, url+address, message)
	if err != nil {
		return err
	}
	if len(responseBytes) > 0 {
		err = s.writeString(responseBytes)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *webSocket) handleOutgoingMessage(message string) error {
	// handle message
	return s.writeString(message)
}
