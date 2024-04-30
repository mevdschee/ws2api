package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"time"

	"github.com/avast/retry-go"
	"github.com/lxzan/gws"
)

// fetchDataWithRetries is your wrapped retrieval.
// It works with a static configuration for the retries,
// but obviously, you can generalize this function further.
func fetchDataWithRetries(c *http.Client, url string) (r *http.Response, err error) {
	retry.Do(
		// The actual function that does "stuff"
		func() error {
			r, err = c.Get(url)
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

func main() {
	upgrader := gws.NewUpgrader(&Handler{}, &gws.ServerOption{
		CheckUtf8Enabled: true,
		Recovery:         gws.Recovery,
		PermessageDeflate: gws.PermessageDeflate{
			Enabled:               true,
			ServerContextTakeover: true,
			ClientContextTakeover: true,
		},
	})
	http.HandleFunc("/connect", func(writer http.ResponseWriter, request *http.Request) {
		socket, err := upgrader.Upgrade(writer, request)
		if err != nil {
			return
		}
		go func() {
			socket.ReadLoop()
		}()
	})
	log.Panic(
		http.ListenAndServe(":4000", nil),
	)
}

type Handler struct {
	gws.BuiltinEventHandler
	jar *cookiejar.Jar
}

func (c *Handler) OnPing(socket *gws.Conn, payload []byte) {
	_ = socket.WritePong(payload)
}

func (c *Handler) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	if c.jar == nil {
		c.jar, _ = cookiejar.New(nil)
	}
	client := &http.Client{
		Jar: c.jar,
	}
	resp, err := fetchDataWithRetries(client, "http://localhost:5000")
	if err != nil {
		_ = socket.WriteMessage(message.Opcode, []byte("connect failed"))
	}
	b := []byte{}
	if err == nil {
		b, err = ioutil.ReadAll(resp.Body)
		if err != nil {
			_ = socket.WriteMessage(message.Opcode, []byte("read failed"))
		}
		resp.Body.Close()
	}
	//time.Sleep(1000 * time.Millisecond)
	_ = socket.WriteMessage(message.Opcode, b)
}
