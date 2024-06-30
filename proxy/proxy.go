package main

import (
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"runtime"
	"runtime/pprof"
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
func fetchDataWithRetries(c *http.Client, url string, body string) (responseBytes []byte, err error) {
	retry.Do(
		// The actual function that does "stuff"
		func() error {
			r, err := c.Post(url, "application/json", strings.NewReader(body))
			if err == nil {
				defer r.Body.Close()
				responseBytes, err = io.ReadAll(r.Body)
			}
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
		incomingMessages: gws.NewConcurrentMap[*gws.Conn, *chan []byte](16),
		outgoingMessages: gws.NewConcurrentMap[*gws.Conn, *chan []byte](16),
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
				if len(parts) != 5 {
					writer.WriteHeader(400)
					writer.Write([]byte("invalid url, use /type/message/address/guid"))
					return
				}
				msgAction := parts[2]
				address := parts[3]
				msgId := parts[4]
				// find socket
				socket, ok := handler.sessions.Load(address)
				if !ok {
					writer.WriteHeader(404)
					writer.Write([]byte("could not find address"))
					return
				}
				defer request.Body.Close()
				bodyBytes, err := io.ReadAll(request.Body)
				if err != nil {
					writer.WriteHeader(500)
					writer.Write([]byte("could not read body"))
					return
				}
				outgoingMessages, ok := handler.outgoingMessages.Load(socket)
				if !ok {
					writer.WriteHeader(500)
					writer.Write([]byte("could not find outgoing channel"))
					return
				}
				outgoingActions, ok := handler.outgoingActions.Load(socket)
				if !ok {
					writer.WriteHeader(500)
					writer.Write([]byte("could not find actions map"))
					return
				}
				(*outgoingActions)[msgId] = msgAction
				*outgoingMessages <- []byte("[" + string(CALL) + ",\"" + msgId + "\",\"" + msgAction + "\"," + string(bodyBytes) + "]")
			default:
				// parse address
				if len(parts[1]) == 0 {
					for k, v := range handler.statistics.counters {
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
				handler.statistics.increment("addresses")
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
	incomingMessages *gws.ConcurrentMap[*gws.Conn, *chan []byte]
	outgoingMessages *gws.ConcurrentMap[*gws.Conn, *chan []byte]
}

func (c *Handler) handleConnection(socket *gws.Conn, address string) {
	c.sessions.Store(address, socket)
	outgoingActions := make(map[string]string)
	c.outgoingActions.Store(socket, &outgoingActions)
	c.addresses.Store(socket, address)
	incomingMessages := make(chan []byte)
	outgoingMessages := make(chan []byte)
	c.incomingMessages.Store(socket, &incomingMessages)
	c.outgoingMessages.Store(socket, &outgoingMessages)
	go c.handleIncomingMessages(socket, &incomingMessages)
	go c.handleOutgoingMessages(socket, &outgoingMessages)
	socket.ReadLoop()
	c.addresses.Delete(socket)
	c.addresses.Delete(socket)
	c.incomingMessages.Delete(socket)
	c.outgoingMessages.Delete(socket)
}

func (c *Handler) handleIncomingMessages(socket *gws.Conn, incomingMessages *chan []byte) {
	address, ok := c.addresses.Load(socket)
	if !ok {
		log.Fatalln("could not find address")
	}
	client := &http.Client{}
	for msg := range *incomingMessages {
		msgType := msg[1]
		switch msgType {
		case CALL:
			fields := strings.SplitN(string(msg[1:len(msg)-1]), ",", 4)
			msgId := strings.Trim(fields[1], "\"")
			msgAction := strings.Trim(fields[2], "\"")
			msgBody := fields[3]
			c.statistics.increment("request_count")
			c.statistics.increment("curl_count")
			responseBytes, err := fetchDataWithRetries(client, "http://localhost:5000/call/"+msgAction+"/"+address+"/"+msgId, msgBody)
			c.statistics.decrement("curl_count")
			if err != nil {
				socket.WriteString("[" + string(CALLERROR) + ",\"" + msgId + "\",\"InternalError\",\"OnMessage: connect failed\",{}]")
				return
			}
			if err != nil {
				socket.WriteString("[" + string(CALLERROR) + ",\"" + msgId + "\",\"InternalError\",\"OnMessage: read failed\",{}]")
				return
			}
			err = socket.WriteString("[" + string(CALLRESULT) + ",\"" + msgId + "\"," + string(responseBytes) + "]")
			if err != nil {
				log.Println(err.Error())
			}
		case CALLRESULT:
			fields := strings.SplitN(string(msg[1:len(msg)-1]), ",", 3)
			msgId := strings.Trim(fields[1], "\"")
			msgBody := fields[2]
			outgoingActions, ok := c.outgoingActions.Load(socket)
			if !ok {
				log.Println("could not find message action")
			}
			msgAction, ok := (*outgoingActions)[msgId]
			if ok {
				delete((*outgoingActions), msgId)
			}
			c.statistics.increment("request_count")
			c.statistics.increment("curl_count")
			_, err := fetchDataWithRetries(client, "http://localhost:5000/result/"+msgAction+"/"+address+"/"+msgId, msgBody)
			c.statistics.decrement("curl_count")
			if err != nil {
				log.Println(err.Error())
			}
		case CALLERROR:
			fields := strings.SplitN(string(msg[1:len(msg)-1]), ",", 5)
			msgId := strings.Trim(fields[1], "\"")
			msgBody := "{\"code\":" + fields[2] + ",\"description\":" + fields[3] + ",\"details\":" + fields[4] + "}"
			outgoingActions, ok := c.outgoingActions.Load(socket)
			if !ok {
				log.Println("could not find message action")
			}
			msgAction, ok := (*outgoingActions)[msgId]
			if ok {
				delete((*outgoingActions), msgId)
			}
			c.statistics.increment("request_count")
			c.statistics.increment("curl_count")
			_, err := fetchDataWithRetries(client, "http://localhost:5000/error/"+msgAction+"/"+address+"/"+msgId, msgBody)
			c.statistics.decrement("curl_count")
			if err != nil {
				log.Println(err.Error())
			}
		}
	}
}

func (c *Handler) handleOutgoingMessages(socket *gws.Conn, outgoingMessages *chan []byte) {
	for msg := range *outgoingMessages {
		err := socket.WriteString(string(msg))
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
	msg := []byte(message.Data.String())
	incomingMessages, ok := c.incomingMessages.Load(socket)
	if !ok {
		log.Println("could not find incoming channel")
	}
	*incomingMessages <- msg
}
