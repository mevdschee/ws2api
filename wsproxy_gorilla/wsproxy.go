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

const (
	CALL       byte = '2' // Client-to-Server
	CALLRESULT byte = '3' // Server-to-Client
	CALLERROR  byte = '4' // Server-to-Client
)

// fetchDataWithRetries is your wrapped retrieval.
// It works with a static configuration for the retries,
// but obviously, you can generalize this function further.
func fetchDataWithRetries(c *http.Client, url string, body string) (message string, err error) {
	retry.Do(
		// The actual function that does "stuff"
		func() error {
			r, err := c.Post(url, "application/json", strings.NewReader(body))
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

func main() {
	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to file")
	memprofile := flag.String("memprofile", "", "write mem profile to file")
	metricsAddress := flag.String("metrics", ":8080", "address to listen for Prometheus metric scraper over HTTP")
	flag.Parse()
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}
	webSocketHandler := webSocketHandler{
		mutex:       &sync.Mutex{},
		upgrader:    websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		readLocks:   map[*websocket.Conn]*sync.Mutex{},
		writeLocks:  map[*websocket.Conn]*sync.Mutex{},
		connections: map[string]*websocket.Conn{},
		msgActions:  map[string]string{},
		metrics:     metrics.New(),
	}
	go webSocketHandler.serve(*memprofile, *metricsAddress)
	http.Handle("/", webSocketHandler)
	log.Print("Starting server...")
	log.Fatal(http.ListenAndServe(":4000", nil))
}

type webSocketHandler struct {
	upgrader    websocket.Upgrader
	mutex       *sync.Mutex
	readLocks   map[*websocket.Conn]*sync.Mutex
	writeLocks  map[*websocket.Conn]*sync.Mutex
	connections map[string]*websocket.Conn
	msgActions  map[string]string
	metrics     *metrics.Metrics
}

func (wsh webSocketHandler) serve(memprofile, metricsAddress string) {
	err := http.ListenAndServe(metricsAddress, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		wsh.metrics.Write(&writer)
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

func (wsh webSocketHandler) getReadLock(c *websocket.Conn) *sync.Mutex {
	wsh.mutex.Lock()
	defer wsh.mutex.Unlock()
	readLock, ok := wsh.readLocks[c]
	if !ok {
		readLock = &sync.Mutex{}
		wsh.readLocks[c] = readLock
	}
	return readLock
}

func (wsh webSocketHandler) getWriteLock(c *websocket.Conn) *sync.Mutex {
	wsh.mutex.Lock()
	defer wsh.mutex.Unlock()
	writeLock, ok := wsh.writeLocks[c]
	if !ok {
		writeLock = &sync.Mutex{}
		wsh.writeLocks[c] = writeLock
	}
	return writeLock
}

func (wsh webSocketHandler) storeConnection(c *websocket.Conn, address string) {
	wsh.mutex.Lock()
	defer wsh.mutex.Unlock()
	wsh.connections[address] = c
}

func (wsh webSocketHandler) retrieveConnection(address string) *websocket.Conn {
	wsh.mutex.Lock()
	defer wsh.mutex.Unlock()
	return wsh.connections[address]
}

func (wsh webSocketHandler) storeMsgAction(msgId string, msgAction string) {
	wsh.mutex.Lock()
	defer wsh.mutex.Unlock()
	wsh.msgActions[msgId] = msgAction
}

func (wsh webSocketHandler) retrieveMsgAction(msgId string) string {
	wsh.mutex.Lock()
	defer wsh.mutex.Unlock()
	return wsh.msgActions[msgId]
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
		c := wsh.retrieveConnection(address)
		if c == nil {
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
		wsh.handleOutgoingMessage(c, message)
	default: // get
		if r.Header.Get("Upgrade") == "" {
			w.WriteHeader(500)
			w.Write([]byte("no upgrade requested"))
			return
		}
		client := &http.Client{}
		responseBytes, err := fetchDataWithRetries(client, "http://localhost:5000/connect", address)
		if err != nil {
			log.Printf("error %s when proxying connect", err)
			return
		}
		if responseBytes != "\"ok\"" {
			log.Printf("not allowed to connect: %s", responseBytes)
			return
		}
		c, err := wsh.upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("error %s when upgrading connection to websocket", err)
			return
		}
		defer c.Close()
		wsh.storeConnection(c, address)
		wsh.metrics.Inc("wsproxy_connection", "event", "start", 1)
		for {
			wsh.metrics.Inc("wsproxy_message", "event", "start", 1)
			err = wsh.handleIncomingMessage(c, address, client)
			wsh.metrics.Inc("wsproxy_message", "event", "finish", 1)
			if err != nil {
				log.Printf("error %s", err)
				break
			}
		}
		wsh.metrics.Inc("wsproxy_connection", "event", "finish", 1)
	}
}

func (wsh webSocketHandler) readString(c *websocket.Conn) (string, error) {
	readLock := wsh.getReadLock(c)
	readLock.Lock()
	defer readLock.Unlock()
	mt, msg, err := c.ReadMessage()
	if err != nil {
		return "", err
	}
	if mt == websocket.BinaryMessage {
		return "", fmt.Errorf("binary messages not supported")
	}
	return string(msg), nil
}

func (wsh webSocketHandler) writeString(c *websocket.Conn, message string) error {
	writeLock := wsh.getWriteLock(c)
	writeLock.Lock()
	defer writeLock.Unlock()
	return c.WriteMessage(websocket.TextMessage, []byte(message))
}

func (wsh webSocketHandler) handleIncomingMessage(c *websocket.Conn, address string, client *http.Client) error {
	// receive message
	message, err := wsh.readString(c)
	if err != nil {
		return err
	}
	//log.Printf("Receive message %s", message)
	// handle message
	fields := strings.Split(message[1:len(message)-1], ",")
	msgType := message[1]
	msgId := strings.Trim(fields[1], "\"")
	switch msgType {
	case CALL:
		msgAction := strings.Trim(fields[2], "\"")
		wsh.storeMsgAction(msgId, msgAction)
		start := time.Now()
		responseBytes, err := fetchDataWithRetries(client, "http://localhost:5000/call/"+msgAction+"/"+address+"/"+msgId, message)
		wsh.metrics.Add("wsproxy_call_message", "msgAction", msgAction, time.Since(start).Seconds())
		if err != nil {
			wsh.writeString(c, "["+string(CALLERROR)+",\""+msgId+"\",\"InternalError\",\"connect failed\",{}]")
			log.Println(err.Error())
			return nil
		}
		err = wsh.writeString(c, "["+string(CALLRESULT)+",\""+msgId+"\","+responseBytes+"]")
		if err != nil {
			log.Println(err.Error())
		}
	case CALLRESULT:
		msgAction := wsh.retrieveMsgAction(msgId)
		start := time.Now()
		_, err := fetchDataWithRetries(client, "http://localhost:5000/result/"+msgAction+"/"+address+"/"+msgId, message)
		wsh.metrics.Add("wsproxy_call_result_message", "msgAction", msgAction, time.Since(start).Seconds())
		if err != nil {
			log.Println(err.Error())
		}
	case CALLERROR:
		msgAction := wsh.retrieveMsgAction(msgId)
		start := time.Now()
		_, err := fetchDataWithRetries(client, "http://localhost:5000/error/"+msgAction+"/"+address+"/"+msgId, message)
		wsh.metrics.Add("wsproxy_call_error_message", "msgAction", msgAction, time.Since(start).Seconds())
		if err != nil {
			log.Println(err.Error())
		}
	}
	return nil
}

func (wsh webSocketHandler) handleOutgoingMessage(c *websocket.Conn, message string) {
	// handle message
	msgType := message[1]
	switch msgType {
	case CALL:
		wsh.metrics.Inc("wsproxy_messages_out", "msgType", string(msgType), 1)
		err := wsh.writeString(c, message)
		if err != nil {
			log.Println(err.Error())
		}
	default:
		log.Printf("message type not accepted: '%s'\n", string(msgType))
	}
}
