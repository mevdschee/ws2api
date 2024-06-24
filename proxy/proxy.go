package main

import (
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/avast/retry-go"
	"github.com/lxzan/gws"
)

// func init() {
// 	runtime.GOMAXPROCS(8)
// }

const (
	CALL       string = "2" // Client-to-Server
	CALLRESULT string = "3" // Server-to-Client
	CALLERROR  string = "4" // Server-to-Client
)

// fetchDataWithRetries is your wrapped retrieval.
// It works with a static configuration for the retries,
// but obviously, you can generalize this function further.
func fetchDataWithRetries(c *http.Client, url string, body string) (r *http.Response, err error) {
	retry.Do(
		// The actual function that does "stuff"
		func() error {
			r, err = c.Post(url, "application/json", strings.NewReader(body))
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

var request_count = 0
var curl_count = 0
var count_channel chan int

func main() {
	handler := Handler{
		sessions:  gws.NewConcurrentMap[string, *gws.Conn](16),
		addresses: gws.NewConcurrentMap[*gws.Conn, string](16),
		//channels:  gws.NewConcurrentMap[*gws.Conn, *chan []string](16),
	}
	serverOptions := gws.ServerOption{
		CheckUtf8Enabled:  true,
		Recovery:          gws.Recovery,
		PermessageDeflate: gws.PermessageDeflate{Enabled: false},
		// keep disabled to ensure packet order
		//		ParallelEnabled:   true,
		//		ParallelGolimit:   16,
	}
	upgrader := gws.NewUpgrader(&handler, &serverOptions)
	// log session and request counts (start)
	count_channel = make(chan int, 1000000)
	ticker := time.NewTicker(time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				log.Printf("addresses: %v, requests %v, curls %v", handler.addresses.Len(), request_count, curl_count)
			}
		}
	}()
	go func() {
		for {
			select {
			case c := <-count_channel:
				if c > 0 {
					request_count += c
				}
				curl_count += c
				break
			}
		}
	}()
	// log session and request counts (end)
	log.Panic(
		http.ListenAndServe(":4000", http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			parts := strings.Split(request.URL.Path, "/")
			switch request.Method {
			case http.MethodPost:
				// parse address
				if len(parts) != 4 {
					writer.WriteHeader(400)
					writer.Write([]byte("invalid url, use /message/address/guid"))
					return
				}
				msgAction := parts[1]
				address := parts[2]
				msgId := parts[3]
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
				socket.WriteString("[" + CALL + ",\"" + msgId + "\",\"" + msgAction + "\"," + string(bodyBytes) + "]")
				// find channel
				// channel, ok := handler.channels.Load(socket)
				// if !ok {
				// 	writer.WriteHeader(404)
				// 	writer.Write([]byte("could not find channel"))
				// 	return
				// }
				// fields := <-*channel
				// switch fields[0] {
				// case CALLRESULT:
				// 	fallthrough
				// case CALLERROR:
				// 	writer.Write([]byte(fmt.Sprintf("%v", fields)))
				// }
				// reply with contents of next received message (use channel? how to relate?)
			default:
				// parse address
				if len(parts) != 2 {
					writer.WriteHeader(404)
					writer.Write([]byte("invalid url, use /address"))
					return
				}
				address := parts[1]
				socket, err := upgrader.Upgrade(writer, request)
				if err != nil {
					writer.WriteHeader(500)
					writer.Write([]byte("could not upgrade socket"))
					return
				}
				go func() {
					//channel := make(chan []string)
					handler.sessions.Store(address, socket)
					handler.addresses.Store(socket, address)
					//handler.channels.Store(socket, &channel)
					//socket.WriteString(address + "=>" + socket.RemoteAddr().String())
					socket.ReadLoop()
					handler.addresses.Delete(socket)
					//handler.channels.Delete(socket)
				}()
			}
		})),
	)
}

type Handler struct {
	gws.BuiltinEventHandler
	sessions  *gws.ConcurrentMap[string, *gws.Conn]
	addresses *gws.ConcurrentMap[*gws.Conn, string]
	//channels  *gws.ConcurrentMap[*gws.Conn, *chan []string]
}

func (c *Handler) OnPing(socket *gws.Conn, payload []byte) {
	_ = socket.WritePong(payload)
}

func (c *Handler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if message.Opcode == gws.OpcodePing {
		socket.WritePong(message.Bytes())
		return
	}
	msg := message.Data.String()
	msgType := msg[1:2]
	switch msgType {
	case CALL:
		fields := strings.SplitN(msg[1:len(msg)-1], ",", 4)
		msgId := strings.Trim(fields[1], "\"")
		msgAction := strings.Trim(fields[2], "\"")
		msgBody := fields[3]
		address, ok := c.addresses.Load(socket)
		if !ok {
			socket.WriteString("[" + CALLERROR + ",\"" + msgId + "\",\"InternalError\",\"OnMessage: could not find address\",{}]")
			return
		}
		client := &http.Client{}
		count_channel <- 1
		resp, err := fetchDataWithRetries(client, "http://localhost:5000/"+msgAction+"/"+address+"/"+msgId, msgBody)
		count_channel <- -1
		if err != nil {
			socket.WriteString("[" + CALLERROR + ",\"" + msgId + "\",\"InternalError\",\"OnMessage: connect failed\",{}]")
			return
		}
		defer resp.Body.Close()
		responseBytes, err := io.ReadAll(resp.Body)
		if err != nil {
			socket.WriteString("[" + CALLERROR + ",\"" + msgId + "\",\"InternalError\",\"OnMessage: read failed\",{}]")
			return
		}
		//time.Sleep(1000 * time.Millisecond)
		socket.WriteString("[" + CALLRESULT + ",\"" + msgId + "\"," + string(responseBytes) + "]")
		//_ = socket.WriteString(fmt.Sprintf("len: %v\n", c.sessions.Len()))
	case CALLRESULT:
		// fields := strings.SplitN(msg[1:len(msg)-1], ",", 3)
		// msgId := strings.Trim(fields[1], "\"")
		// msgBody := fields[2]
		// channel, _ := c.channels.Load(socket)
		//*channel <- []string{CALLRESULT, msgId, msgBody}
	case CALLERROR:
		//fields := strings.SplitN(msg[1:len(msg)-1], ",", 5)
		// msgId := strings.Trim(fields[1], "\"")
		// errCode := fields[2]
		// errDescription := fields[3]
		// errDetails := fields[4]
		// channel, _ := c.channels.Load(socket)
		//*channel <- []string{CALLERROR, msgId, errCode, errDescription, errDetails}
	}
}
