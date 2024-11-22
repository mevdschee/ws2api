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
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lxzan/gws"
)

func init() {
	runtime.GOMAXPROCS(8)
}

var (
	qps   uint64 = 0
	conns uint64 = 0
)

func fetchData(c *http.Client, url string, body string) (string, error) {
	var r *http.Response
	var err error
	if len(body) == 0 {
		r, err = c.Get(url)
	} else {
		r, err = c.Post(url, "plain/text", strings.NewReader(body))
	}
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	responseBytes, err := io.ReadAll(r.Body)
	responseString := string(responseBytes)
	if err != nil {
		return responseString, err
	}
	if r.StatusCode != 200 {
		return responseString, fmt.Errorf("proxy returned: %s", r.Status)
	}
	return responseString, nil
}

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")
var memprofile = flag.String("memprofile", "", "write mem profile to file")
var statistics = Statistics{counters: map[string]uint64{}}

func increaseNumberOfOpenFiles() {
	if runtime.GOOS == "linux" {
		var rLimit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
			log.Fatalf("failed to get rlimit: %v", err)
		}
		rLimit.Cur = rLimit.Max
		if err := syscall.Setrlimit(syscall.RLIMIT_NOFILE, &rLimit); err != nil {
			log.Fatalf("failed to set rlimit: %v", err)
		}
	}
}

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
	increaseNumberOfOpenFiles()
	go printStatistics()
	log.Println("Proxy running on: http://localhost:4000/")
	log.Panic(http.ListenAndServe(":4000", getWsHandler("http://localhost:5000/")))
}

func getWsHandler(serverUrl string) http.Handler {
	handler := Handler{
		connections: gws.NewConcurrentMap[string, *gws.Conn](16),
		addresses:   gws.NewConcurrentMap[*gws.Conn, string](16),
		upgrader:    nil,
		serverUrl:   serverUrl,
	}
	serverOptions := gws.ServerOption{
		CheckUtf8Enabled:  true,
		Recovery:          gws.Recovery,
		PermessageDeflate: gws.PermessageDeflate{Enabled: false},
		ParallelEnabled:   true,
		ParallelGolimit:   16,
	}
	handler.upgrader = gws.NewUpgrader(&handler, &serverOptions)
	return handler
}

func printStatistics() {
	total := uint64(0)
	ticker := time.NewTicker(time.Second)
	fmt.Printf("seconds,connections,qps,total\n")
	for i := 1; true; i++ {
		<-ticker.C
		n := atomic.SwapUint64(&qps, 0)
		total += n
		fmt.Printf("%v,%v,%v,%v\n", i, atomic.LoadUint64(&conns), n, total)
	}
}

type Statistics struct {
	mutex    sync.RWMutex
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
	connections *gws.ConcurrentMap[string, *gws.Conn]
	addresses   *gws.ConcurrentMap[*gws.Conn, string]
	upgrader    *gws.Upgrader
	serverUrl   string
}

func (c Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	address := strings.Split(request.URL.Path, "/")[1]
	if request.Method == http.MethodPost {
		// find connection
		connection, ok := c.connections.Load(address)
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
		err = connection.WriteString(string(bodyBytes))
		if err != nil {
			log.Println("could not write message")
		}
		writer.Write([]byte("ok"))
		return
	}
	// parse address
	if len(address) == 0 {
		statistics.mutex.RLock()
		defer statistics.mutex.RUnlock()
		for k, v := range statistics.counters {
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
	client := &http.Client{}
	responseBytes, err := fetchData(client, c.serverUrl+address, "")
	if err != nil {
		writer.WriteHeader(502)
		writer.Write([]byte("error when proxying connect"))
		fmt.Println("error when proxying connect")
		return
	}
	if responseBytes != "ok" {
		writer.WriteHeader(403)
		writer.Write([]byte("not allowed to connect"))
		fmt.Println("not allowed to connect")
		return
	}
	if request.Header.Get("Upgrade") != "websocket" {
		writer.WriteHeader(400)
		writer.Write([]byte("no upgrade requested"))
		fmt.Println("no upgrade requested")
		return
	}
	connection, err := c.upgrader.Upgrade(writer, request)
	if err != nil {
		fmt.Println("could not upgrade connection")
		return
	}
	atomic.AddUint64(&conns, 1)
	statistics.increment("addresses")
	c.connections.Store(address, connection)
	c.addresses.Store(connection, address)
	connection.ReadLoop()
	c.addresses.Delete(connection)
	c.addresses.Delete(connection)
}

func (c Handler) OnMessage(connection *gws.Conn, message *gws.Message) {
	defer message.Close()
	atomic.AddUint64(&qps, 1)
	if message.Opcode == gws.OpcodeBinary {
		log.Println("binary messages not supported")
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
			fmt.Println("could not find address")
		}
		client := &http.Client{}
		statistics.increment("request_count")
		statistics.increment("curl_count")
		responseBytes, err := fetchData(client, c.serverUrl+address, msg)
		if err != nil {
			fmt.Println(err.Error())
		}
		statistics.decrement("curl_count")
		err = connection.WriteString(string(responseBytes))
		if err != nil {
			log.Println(err.Error())
		}
		return
	}
}
