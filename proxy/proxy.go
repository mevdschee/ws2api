package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/avast/retry-go"
	"github.com/lxzan/gws"
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

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")
var memprofile = flag.String("memprofile", "", "write mem profile to file")

func main() {
	flag.Parse()
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
	}
	handler := Handler{
		sessions:         gws.NewConcurrentMap[string, *gws.Conn](16),
		statistics:       Statistics{counters: map[string]uint64{}},
		addresses:        gws.NewConcurrentMap[*gws.Conn, string](16),
		outgoingActions:  gws.NewConcurrentMap[*gws.Conn, *map[string]string](16),
		incomingMessages: gws.NewConcurrentMap[*gws.Conn, *chan string](16),
		outgoingMessages: gws.NewConcurrentMap[*gws.Conn, *chan string](16),
	}
	serverOptions := gws.ServerOption{
		CheckUtf8Enabled:  true,
		Recovery:          gws.Recovery,
		PermessageDeflate: gws.PermessageDeflate{Enabled: false},
		// keep disabled to ensure packet order
		ParallelEnabled: true,
		ParallelGolimit: 16,
	}
	upgrader := gws.NewUpgrader(&handler, &serverOptions)
	log.Println("stats on: http://localhost:4000/")
	log.Panic(
		http.ListenAndServe(":4000", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			parts := strings.Split(request.URL.Path, "/")
			switch request.Method {
			case http.MethodPost:
				// parse address
				address := parts[1]
				if len(parts) != 2 || len(address) == 0 {
					writer.WriteHeader(400)
					writer.Write([]byte("invalid url, use /address"))
					return
				}
				// find socket
				socket, ok := handler.sessions.Load(address)
				if !ok {
					writer.WriteHeader(404)
					writer.Write([]byte("could not find address"))
					return
				}
				requestBody, err := io.ReadAll(request.Body)
				request.Body.Close()
				msg := string(requestBody)
				if err != nil {
					writer.WriteHeader(500)
					writer.Write([]byte("could not read body"))
					return
				}
				fields := strings.Split(msg[1:len(msg)-1], ",")
				msgType := msg[1]
				msgId := strings.Trim(fields[1], "\"")
				switch msgType {
				case CALL:
					msgAction := strings.Trim(fields[2], "\"")
					outgoingActions, ok := handler.outgoingActions.Load(socket)
					if !ok {
						writer.WriteHeader(500)
						writer.Write([]byte("could not find actions map"))
						return
					}
					(*outgoingActions)[msgId] = msgAction
				}
				outgoingMessages, ok := handler.outgoingMessages.Load(socket)
				if !ok {
					writer.WriteHeader(500)
					writer.Write([]byte("could not find outgoing channel"))
					return
				}
				*outgoingMessages <- msg
			default:
				// parse address
				if len(parts[1]) == 0 {
					var keys []string
					for key := range handler.statistics.counters {
						keys = append(keys, key)
					}
					sort.Strings(keys)
					for _, k := range keys {
						v := handler.statistics.counters[k]
						writer.Write([]byte(k + " " + strconv.FormatUint(v, 10) + "\n"))
					}
					if *memprofile != "" {
						f, err := os.Create(*memprofile)
						if err != nil {
							log.Fatal(err)
						}
						pprof.WriteHeapProfile(f)
						f.Close()
					}
					return
				}
				address := parts[1]
				socket, err := upgrader.Upgrade(writer, request)
				if err != nil {
					writer.WriteHeader(500)
					writer.Write([]byte("could not upgrade socket"))
					return
				}
				go handler.handleConnection(socket, address)
			}
		})),
	)
}

type Statistics struct {
	mutex    sync.Mutex
	counters map[string]uint64
}

func (s *Statistics) increment(name string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.counters[name]++
}

func (s *Statistics) decrement(name string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.counters[name]--
}

type Handler struct {
	gws.BuiltinEventHandler
	statistics       Statistics
	sessions         *gws.ConcurrentMap[string, *gws.Conn]
	outgoingActions  *gws.ConcurrentMap[*gws.Conn, *map[string]string]
	addresses        *gws.ConcurrentMap[*gws.Conn, string]
	incomingMessages *gws.ConcurrentMap[*gws.Conn, *chan string]
	outgoingMessages *gws.ConcurrentMap[*gws.Conn, *chan string]
}

func (c *Handler) handleConnection(socket *gws.Conn, address string) {
	c.sessions.Store(address, socket)
	outgoingActions := make(map[string]string)
	c.outgoingActions.Store(socket, &outgoingActions)
	c.addresses.Store(socket, address)
	incomingMessages := make(chan string)
	outgoingMessages := make(chan string)
	c.incomingMessages.Store(socket, &incomingMessages)
	c.outgoingMessages.Store(socket, &outgoingMessages)
	go c.handleIncomingMessages(socket, &incomingMessages)
	go c.handleOutgoingMessages(socket, &outgoingMessages)
	c.statistics.increment("connections")
	c.statistics.increment("connections_active")
	socket.ReadLoop()
	c.statistics.decrement("connections_active")
	c.addresses.Delete(socket)
	c.addresses.Delete(socket)
	c.incomingMessages.Delete(socket)
	c.outgoingMessages.Delete(socket)
}

func (c *Handler) handleIncomingMessages(socket *gws.Conn, incomingMessages *chan string) {
	address, ok := c.addresses.Load(socket)
	if !ok {
		log.Fatalln("could not find address")
	}
	client := &http.Client{}
	for msg := range *incomingMessages {
		c.statistics.increment("messages")
		fields := strings.Split(string(msg[1:len(msg)-1]), ",")
		msgType := msg[1]
		msgId := strings.Trim(fields[1], "\"")
		switch msgType {
		case CALL:
			msgAction := strings.Trim(fields[2], "\"")
			c.statistics.increment("http_requests")
			c.statistics.increment("http_requests_active")
			responseBytes, err := fetchDataWithRetries(client, "http://localhost:5000/call/"+msgAction+"/"+address+"/"+msgId, msg)
			c.statistics.decrement("http_requests_active")
			if err != nil {
				socket.WriteString("[" + string(CALLERROR) + ",\"" + msgId + "\",\"InternalError\",\"connect failed\",{}]")
				return
			}
			err = socket.WriteString("[" + string(CALLRESULT) + ",\"" + msgId + "\"," + string(responseBytes) + "]")
			if err != nil {
				log.Println(err.Error())
			}
		case CALLRESULT:
			outgoingActions, ok := c.outgoingActions.Load(socket)
			if !ok {
				log.Println("could not find message action")
			}
			msgAction, ok := (*outgoingActions)[msgId]
			if ok {
				delete((*outgoingActions), msgId)
			}
			c.statistics.increment("http_requests")
			c.statistics.increment("http_requests_active")
			_, err := fetchDataWithRetries(client, "http://localhost:5000/result/"+msgAction+"/"+address+"/"+msgId, msg)
			c.statistics.decrement("http_requests_active")
			if err != nil {
				log.Println(err.Error())
			}
		case CALLERROR:
			outgoingActions, ok := c.outgoingActions.Load(socket)
			if !ok {
				log.Println("could not find message action")
			}
			msgAction, ok := (*outgoingActions)[msgId]
			if ok {
				delete((*outgoingActions), msgId)
			}
			c.statistics.increment("http_requests")
			c.statistics.increment("http_requests_active")
			_, err := fetchDataWithRetries(client, "http://localhost:5000/error/"+msgAction+"/"+address+"/"+msgId, msg)
			c.statistics.decrement("http_requests_active")
			if err != nil {
				log.Println(err.Error())
			}
		}
	}
}

func (c *Handler) handleOutgoingMessages(socket *gws.Conn, outgoingMessages *chan string) {
	for msg := range *outgoingMessages {
		err := socket.WriteString(msg)
		if err != nil {
			log.Println(err.Error())
		}
	}
}

func (c *Handler) OnPing(socket *gws.Conn, payload []byte) {
	err := socket.WritePong(payload)
	if err != nil {
		log.Println(err.Error())
	}
}

func (c *Handler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if message.Opcode == gws.OpcodePing {
		err := socket.WritePong(message.Bytes())
		if err != nil {
			log.Println(err.Error())
		}
		return
	}
	msg := message.Data.String()
	incomingMessages, ok := c.incomingMessages.Load(socket)
	if !ok {
		log.Println("could not find incoming channel")
	}
	*incomingMessages <- msg
}
