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
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/lxzan/gws"
)

func init() {
	runtime.GOMAXPROCS(8)
}

// var (
// 	rps   uint64 = 0
// 	conns uint64 = 0
// )

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")
var memprofile = flag.String("memprofile", "", "write mem profile to file")

// func increaseNumberOfOpenFiles() {
// 	var rLimit syscall.Rlimit
// 	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
// 		log.Fatalf("failed to get rlimit: %v", err)
// 	}
// 	rLimit.Cur = rLimit.Max
// 	if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
// 		log.Fatalf("failed to set rlimit: %v", err)
// 	}
// }

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
	//increaseNumberOfOpenFiles()
	//go printStatistics()
	log.Println("Proxy running on: http://localhost:7001/")
	log.Panic(http.ListenAndServe(":7001", getWsHandler("http://localhost:8000/wsoverhttp/")))
}

func getWsHandler(serverUrl string) http.Handler {
	handler := Handler{
		connections: gws.NewConcurrentMap[string, *gws.Conn](16),
		addresses:   gws.NewConcurrentMap[*gws.Conn, string](16),
		upgrader:    nil,
		serverUrl:   serverUrl,
		statistics:  Statistics{},
		client:      nil,
	}
	serverOptions := gws.ServerOption{
		CheckUtf8Enabled:  true,
		Recovery:          gws.Recovery,
		PermessageDeflate: gws.PermessageDeflate{Enabled: false},
		ParallelEnabled:   true,
		ParallelGolimit:   16,
	}
	handler.upgrader = gws.NewUpgrader(&handler, &serverOptions)
	handler.client = handler.httpClient()
	return &handler
}

// func printStatistics() {
// 	total := uint64(0)
// 	ticker := time.NewTicker(time.Second)
// 	log.Printf("seconds,connections,rps,total\n")
// 	for i := 1; true; i++ {
// 		<-ticker.C
// 		n := atomic.SwapUint64(&rps, 0)
// 		total += n
// 		log.Printf("%v,%v,%v,%v\n", i, atomic.LoadUint64(&conns), n, total)
// 	}
// }

type Statistics struct {
	requestsStarted   uint64
	requestsFailed    uint64
	requestsSucceeded uint64
	connectionsOpened uint64
	connectionsClosed uint64
}

type Handler struct {
	gws.BuiltinEventHandler
	connections *gws.ConcurrentMap[string, *gws.Conn]
	addresses   *gws.ConcurrentMap[*gws.Conn, string]
	upgrader    *gws.Upgrader
	serverUrl   string
	statistics  Statistics
	client      *http.Client
}

func (c *Handler) httpClient() *http.Client {
	client := &http.Client{
		Transport: &http.Transport{
			MaxConnsPerHost:     10000, // c10k I guess
			MaxIdleConnsPerHost: 1000,  // just guessing
		},
		Timeout: 60 * time.Second,
	}
	return client
}

func (c *Handler) fetchData(client *http.Client, method, url, body string) (string, error) {
	var r *http.Response
	var err error
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	atomic.AddUint64(&c.statistics.requestsStarted, 1)
	r, err = client.Do(req)
	//log.Printf("curl %s %s", url, body)
	if err != nil {
		atomic.AddUint64(&c.statistics.requestsFailed, 1)
		return "", fmt.Errorf("fetchData: %s", err.Error())
	}
	defer r.Body.Close()
	responseBytes, err := io.ReadAll(r.Body)
	responseString := string(responseBytes)
	//log.Printf("return %d %s", r.StatusCode, responseBytes)
	if err != nil {
		atomic.AddUint64(&c.statistics.requestsFailed, 1)
		return responseString, fmt.Errorf("fetchData: %s", err.Error())
	}
	if r.StatusCode != 200 {
		atomic.AddUint64(&c.statistics.requestsFailed, 1)
		return responseString, fmt.Errorf("fetchData: %s", r.Status)
	}
	atomic.AddUint64(&c.statistics.requestsSucceeded, 1)
	return responseString, nil
}

func (c *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	address := strings.Split(request.URL.Path, "/")[1]
	if request.Method == http.MethodPost {
		// find connection
		connection, ok := c.connections.Load(address)
		if !ok {
			writer.WriteHeader(404)
			writer.Write([]byte("not found"))
			log.Printf("MethodPost: could not find connection: %s", address)
			return
		}
		defer request.Body.Close()
		bodyBytes, err := io.ReadAll(request.Body)
		if err != nil {
			writer.WriteHeader(500)
			writer.Write([]byte("internal server error"))
			log.Println("MethodPost: could not read body")
			return
		}
		err = connection.WriteString(string(bodyBytes))
		if err != nil {
			log.Println("MethodPost: could not write message")
		}
		writer.Write([]byte("ok"))
		return
	}
	// parse address
	if len(address) == 0 {
		writer.Write([]byte("connections_opened " + strconv.FormatUint(atomic.LoadUint64(&c.statistics.connectionsOpened), 10) + "\n"))
		writer.Write([]byte("connections_closed " + strconv.FormatUint(atomic.LoadUint64(&c.statistics.connectionsClosed), 10) + "\n"))
		writer.Write([]byte("requests_started " + strconv.FormatUint(atomic.LoadUint64(&c.statistics.requestsStarted), 10) + "\n"))
		writer.Write([]byte("requests_failed " + strconv.FormatUint(atomic.LoadUint64(&c.statistics.requestsFailed), 10) + "\n"))
		writer.Write([]byte("requests_succeeded " + strconv.FormatUint(atomic.LoadUint64(&c.statistics.requestsSucceeded), 10) + "\n"))
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
	responseBytes, err := c.fetchData(c.client, "GET", c.serverUrl+address, "")
	if err != nil {
		writer.WriteHeader(502)
		writer.Write([]byte("bad gateway"))
		log.Printf("MethodGet: %s", err.Error())
		return
	}
	if responseBytes != "ok" {
		writer.WriteHeader(403)
		writer.Write([]byte("forbidden"))
		log.Println("MethodGet: not allowed to connect")
		return
	}
	if request.Header.Get("Upgrade") != "websocket" {
		writer.WriteHeader(400)
		writer.Write([]byte("no upgrade requested"))
		log.Println("MethodGet: no upgrade requested")
		return
	}
	connection, err := c.upgrader.Upgrade(writer, request)
	if err != nil {
		log.Println("MethodGet: could not upgrade connection")
		return
	}
	//atomic.AddUint64(&conns, 1)
	atomic.AddUint64(&c.statistics.connectionsOpened, 1)
	c.connections.Store(address, connection)
	c.addresses.Store(connection, address)
	connection.ReadLoop()
	c.connections.Delete(address)
	c.addresses.Delete(connection)
	atomic.AddUint64(&c.statistics.connectionsClosed, 1)
}

func (c *Handler) OnMessage(connection *gws.Conn, message *gws.Message) {
	defer message.Close()
	//atomic.AddUint64(&rps, 1)
	if message.Opcode == gws.OpcodeBinary {
		log.Println("OnMessage: binary messages not supported")
		return
	}
	if message.Opcode == gws.OpcodePing {
		err := connection.WritePong(message.Bytes())
		if err != nil {
			log.Println(err.Error())
		}
		return
	}
	if message.Opcode == gws.OpcodeText {
		msg := message.Data.String()
		address, ok := c.addresses.Load(connection)
		if !ok {
			log.Println("OnMessage: could not find address")
			return
		}
		err := error(nil)
		responseBytes, err := c.fetchData(c.client, "POST", c.serverUrl+address, msg)
		if err != nil {
			log.Println(err.Error())
		}
		err = connection.WriteString(responseBytes)
		if err != nil {
			log.Println(err.Error())
		}
		return
	}
}

func (c *Handler) OnClose(connection *gws.Conn, err error) {
	address, ok := c.addresses.Load(connection)
	if !ok {
		log.Printf("OnClose: could not find address")
		return
	}
	reason := err.Error()
	log.Printf("OnClose: address=%s error=%s", address, reason)
	// this should be rate limited
	closeErr, ok := err.(*gws.CloseError)
	if ok {
		reason = string(closeErr.Reason)
	}
	responseBytes, err := c.fetchData(c.client, "DELETE", c.serverUrl+address, reason)
	if err != nil {
		log.Println(err.Error())
	}
	if responseBytes != "ok" {
		log.Println("could not disconnect")
	}
}
