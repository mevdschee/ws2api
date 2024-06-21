package main

import (
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/avast/retry-go"
	"github.com/lxzan/gws"
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
		sessions: gws.NewConcurrentMap[string, *gws.Conn](16),
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
	http.HandleFunc("/connect", func(writer http.ResponseWriter, request *http.Request) {
		socket, err := upgrader.Upgrade(writer, request)
		if err != nil {
			return
		}
		go func() {
			handler.sessions.Store(request.RemoteAddr, socket)
			socket.WriteString(request.RemoteAddr)
			socket.ReadLoop()
			handler.sessions.Delete(request.RemoteAddr)
		}()
	})
	http.HandleFunc("/send", func(writer http.ResponseWriter, request *http.Request) {
		socket, ok := handler.sessions.Load(request.URL.Query()["addr"][0])
		if !ok {
			writer.Write([]byte("could not find socket"))
			return
		}
		b, _ := io.ReadAll(request.Body)
		_ = socket.WriteString(string(b))
	})
	// log session and request counts (start)
	count_channel = make(chan int, 1000000)
	ticker := time.NewTicker(time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				log.Printf("sessions: %v, requests %v, curls %v", handler.sessions.Len(), request_count, curl_count)
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
		http.ListenAndServe(":4000", nil),
	)
}

type Handler struct {
	gws.BuiltinEventHandler
	sessions *gws.ConcurrentMap[string, *gws.Conn]
}

func (c *Handler) OnPing(socket *gws.Conn, payload []byte) {
	_ = socket.WritePong(payload)
}

func (c *Handler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	client := &http.Client{}
	count_channel <- 1
	resp, err := fetchDataWithRetries(client, "http://localhost:5000?addr="+url.QueryEscape(socket.RemoteAddr().String()), message.Data.String())
	count_channel <- -1
	if err != nil {
		_ = socket.WriteMessage(message.Opcode, []byte("connect failed"))
	}
	b := []byte{}
	if err == nil {
		b, err = io.ReadAll(resp.Body)
		if err != nil {
			_ = socket.WriteMessage(message.Opcode, []byte("read failed"))
		}
		resp.Body.Close()
	}
	//time.Sleep(1000 * time.Millisecond)
	_ = socket.WriteMessage(message.Opcode, b)
	//_ = socket.WriteString(fmt.Sprintf("len: %v\n", c.sessions.Len()))
}
