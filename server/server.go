package main

import (
	"io/ioutil"
	"log"
	"net/http"
	"net/http/cookiejar"
	"time"

	"github.com/lxzan/gws"
)

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
	resp, err := client.Get("http://localhost:4000")
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
	time.Sleep(1000 * time.Millisecond)
	_ = socket.WriteMessage(message.Opcode, b)
}
