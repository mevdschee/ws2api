package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"sync"

	"github.com/lxzan/gws"
)

func main() {
	n := 120000
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 1; i <= n; i++ {
		c := new(WebSocket)
		socket, _, err := gws.NewClient(c, &gws.ClientOption{
			Addr:              "ws://127.0.0." + strconv.Itoa(i%255+1) + ":4000/connect" + strconv.Itoa(i),
			PermessageDeflate: gws.PermessageDeflate{Enabled: false},
		})
		if err != nil {
			log.Print(err.Error())
			return
		}
		go socket.ReadLoop()
		go func() {
			c.stress(socket)
			wg.Done()
		}()
	}
	wg.Wait()
}

type WebSocket struct {
}

func (c *WebSocket) stress(socket *gws.Conn) {
	for j := 1; j <= 20000; j++ {
		b, _ := json.Marshal([]any{2, "123", "hello", "hello world" + strconv.Itoa(j)})
		socket.WriteString(string(b))
	}
}

func (c *WebSocket) OnClose(socket *gws.Conn, err error) {
	fmt.Printf("onerror: err=%s\n", err.Error())
}

func (c *WebSocket) OnPong(socket *gws.Conn, payload []byte) {
}

func (c *WebSocket) OnOpen(socket *gws.Conn) {
	//_ = socket.WriteString("hello, there is client")
}

func (c *WebSocket) OnPing(socket *gws.Conn, payload []byte) {
	_ = socket.WritePong(payload)
}

func (c *WebSocket) OnMessage(socket *gws.Conn, message *gws.Message) {
	defer message.Close()
	//fmt.Printf("recv: %s\n", message.Data.String())
}
