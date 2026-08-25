// Command wsproxy terminates WebSocket connections and turns them into plain
// HTTP requests against an API server, so that the API tier can stay stateless.
//
//	WS client --[upgrade]--> wsproxy --[GET    /<ClientId>]--> API server
//	WS client --[message]--> wsproxy --[POST   /<ClientId>]--> API server
//	WS client --[close]----> wsproxy --[DELETE /<ClientId>]--> API server
//	WS client <--[message]-- wsproxy <--[POST   /<ClientId>]-- API server
//
// See README.md for the protocol.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/lxzan/gws"
)

var (
	rps   uint64 = 0
	conns uint64 = 0
)

// Config is everything the proxy needs to run. It is passed in rather than read
// from globals so that a test can start a proxy without touching the process,
// and so that a deployment configures it instead of rebuilding it.
type Config struct {
	// Listen is the address the proxy serves on.
	Listen string
	// APIURL is the prefix a ClientId is appended to. It must end in a slash,
	// because the ClientId is concatenated onto it.
	APIURL string
	// MaxBodyBytes caps a server-to-client push and an API response. Without it
	// one request can make the proxy buffer as much as the sender cares to send.
	MaxBodyBytes int64
	// APITimeout bounds one call to the API server.
	APITimeout time.Duration
	// StatsInterval logs a CSV throughput line on a ticker. Zero disables it,
	// which is what a long-running deployment wants; the benchmark in the
	// README is what it exists for.
	StatsInterval time.Duration
	// MemProfile is where GET /debug/memprofile writes a heap profile.
	MemProfile string
}

const (
	defaultListen       = ":7001"
	defaultAPIURL       = "http://localhost:8000/wsoverhttp/"
	defaultMaxBodyBytes = 1 << 20
	defaultAPITimeout   = 60 * time.Second
)

var (
	listenFlag     = flag.String("listen", envOr("WS2API_LISTEN", defaultListen), "address to serve on")
	apiFlag        = flag.String("api", envOr("WS2API_URL", defaultAPIURL), "API server URL a ClientId is appended to; must end in /")
	maxBodyFlag    = flag.Int64("max-body", envInt64("WS2API_MAX_BODY", defaultMaxBodyBytes), "maximum bytes accepted in one message or response")
	apiTimeoutFlag = flag.Duration("api-timeout", envDuration("WS2API_TIMEOUT", defaultAPITimeout), "timeout for one call to the API server")
	statsFlag      = flag.Duration("stats-interval", envDuration("WS2API_STATS_INTERVAL", 0), "log a throughput line on this interval; 0 disables it")
	cpuprofile     = flag.String("cpuprofile", "", "write cpu profile to file")
	memprofile     = flag.String("memprofile", "", "write mem profile to this file on GET /debug/memprofile")
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
		log.Printf("ignoring %s=%q: not a number", key, v)
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
		log.Printf("ignoring %s=%q: not a duration", key, v)
	}
	return fallback
}

func main() {
	flag.Parse()
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal(err)
		}
		defer pprof.StopCPUProfile()
	}

	config := Config{
		Listen:        *listenFlag,
		APIURL:        *apiFlag,
		MaxBodyBytes:  *maxBodyFlag,
		APITimeout:    *apiTimeoutFlag,
		StatsInterval: *statsFlag,
		MemProfile:    *memprofile,
	}
	if err := run(config); err != nil {
		log.Fatal(err)
	}
}

func run(config Config) error {
	handler, err := getWsHandler(config)
	if err != nil {
		return err
	}
	if config.StatsInterval > 0 {
		go printStatistics(config.StatsInterval)
	}

	server := &http.Server{
		Addr:    config.Listen,
		Handler: handler,
		// Only the header timeout is set. A read or write deadline would apply
		// to the connection after it has been hijacked for the WebSocket too,
		// and would kill every socket that stays quiet for longer than it.
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 1)
	go func() {
		log.Printf("proxy listening on %s, forwarding to %s", config.Listen, config.APIURL)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errs <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	select {
	case err := <-errs:
		return err
	case <-stop:
		log.Println("shutting down")
	}

	// Shutdown closes the listener and waits for in-flight HTTP. It does not
	// close hijacked connections, which is every WebSocket, so open sockets end
	// when the process does and clients reconnect.
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdown)
}

// getWsHandler builds the proxy handler, reporting a configuration it cannot
// use rather than failing later in a way that looks like the API is broken.
func getWsHandler(config Config) (http.Handler, error) {
	if config.APIURL == "" {
		return nil, fmt.Errorf("no API URL configured")
	}
	// The ClientId is concatenated onto this, so a missing slash would produce
	// URLs like "http://api:8080/wsoverhttpABC".
	if !strings.HasSuffix(config.APIURL, "/") {
		return nil, fmt.Errorf("API URL %q must end in a slash", config.APIURL)
	}
	if !strings.HasPrefix(config.APIURL, "http://") && !strings.HasPrefix(config.APIURL, "https://") {
		return nil, fmt.Errorf("API URL %q must be http or https", config.APIURL)
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = defaultMaxBodyBytes
	}
	if config.APITimeout <= 0 {
		config.APITimeout = defaultAPITimeout
	}

	handler := Handler{
		connections: gws.NewConcurrentMap[string, *gws.Conn](16),
		addresses:   gws.NewConcurrentMap[*gws.Conn, string](16),
		upgrader:    nil,
		config:      config,
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
	return &handler, nil
}

func printStatistics(interval time.Duration) {
	total := uint64(0)
	ticker := time.NewTicker(interval)
	log.Printf("seconds,connections,rps,total\n")
	for i := 1; true; i++ {
		<-ticker.C
		n := atomic.SwapUint64(&rps, 0)
		total += n
		log.Printf("%v,%v,%v,%v\n", i, atomic.LoadUint64(&conns), n, total)
	}
}

type Statistics struct {
	requestsStarted   uint64
	requestsFailed    uint64
	requestsSucceeded uint64
	connectionsOpened uint64
	connectionsClosed uint64
	connectionsDenied uint64
	messagesDropped   uint64
}

type Handler struct {
	gws.BuiltinEventHandler
	connections *gws.ConcurrentMap[string, *gws.Conn]
	addresses   *gws.ConcurrentMap[*gws.Conn, string]
	upgrader    *gws.Upgrader
	config      Config
	statistics  Statistics
	client      *http.Client
}

func (c *Handler) httpClient() *http.Client {
	client := &http.Client{
		Transport: &http.Transport{
			MaxConnsPerHost:     10000, // c10k I guess
			MaxIdleConnsPerHost: 1000,  // just guessing
		},
		Timeout: c.config.APITimeout,
	}
	return client
}

// claim registers a connection under a ClientId, refusing one already in use.
//
// Storing over an existing entry would leave the earlier connection in the map
// under a key that now points at the newer one. Closing the earlier connection
// would then delete the newer one's entry, and a live socket would stop
// receiving server pushes with nothing in the logs to say why. The shard is
// locked directly because load-then-store is not atomic.
func (c *Handler) claim(address string, connection *gws.Conn) bool {
	sharding := c.connections.GetSharding(address)
	sharding.Lock()
	defer sharding.Unlock()
	if _, taken := sharding.Load(address); taken {
		return false
	}
	sharding.Store(address, connection)
	return true
}

// release removes a registration, but only if the map still points at this
// connection.
func (c *Handler) release(address string, connection *gws.Conn) {
	sharding := c.connections.GetSharding(address)
	sharding.Lock()
	defer sharding.Unlock()
	if current, ok := sharding.Load(address); ok && current == connection {
		sharding.Delete(address)
	}
}

func (c *Handler) fetchData(method, url, body string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), c.config.APITimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return "", err
	}
	atomic.AddUint64(&c.statistics.requestsStarted, 1)
	r, err := c.client.Do(req)
	if err != nil {
		atomic.AddUint64(&c.statistics.requestsFailed, 1)
		return "", fmt.Errorf("fetchData: %s", err.Error())
	}
	defer r.Body.Close()
	responseBytes, err := io.ReadAll(io.LimitReader(r.Body, c.config.MaxBodyBytes))
	responseString := string(responseBytes)
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
	address := clientID(request.URL.Path)
	if request.Method == http.MethodPost {
		c.servePush(writer, request, address)
		return
	}
	if address == "" {
		c.serveStatistics(writer)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/debug/memprofile") {
		c.serveMemProfile(writer)
		return
	}
	c.serveUpgrade(writer, request, address)
}

// clientID is the first path segment. A request path is always rooted, so this
// is the segment after the leading slash, and empty for "/".
func clientID(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

// servePush writes a server-initiated message to a connection.
func (c *Handler) servePush(writer http.ResponseWriter, request *http.Request, address string) {
	connection, ok := c.connections.Load(address)
	if !ok {
		// Not logged as an error: a 404 here is how the API learns a client has
		// gone without anyone having to tell it.
		writer.WriteHeader(http.StatusNotFound)
		writer.Write([]byte("not found"))
		return
	}
	defer request.Body.Close()
	// One byte over the cap is read so that an oversized body is refused rather
	// than silently truncated onto the socket.
	bodyBytes, err := io.ReadAll(io.LimitReader(request.Body, c.config.MaxBodyBytes+1))
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		writer.Write([]byte("internal server error"))
		log.Println("MethodPost: could not read body")
		return
	}
	if int64(len(bodyBytes)) > c.config.MaxBodyBytes {
		writer.WriteHeader(http.StatusRequestEntityTooLarge)
		writer.Write([]byte("payload too large"))
		log.Printf("MethodPost: body over %d bytes for %s", c.config.MaxBodyBytes, address)
		return
	}
	if err := connection.WriteString(string(bodyBytes)); err != nil {
		// Answering "ok" would tell the API the message was delivered when the
		// socket had already broken.
		writer.WriteHeader(http.StatusBadGateway)
		writer.Write([]byte("could not write message"))
		log.Printf("MethodPost: could not write message to %s: %s", address, err.Error())
		return
	}
	writer.Write([]byte("ok"))
}

// serveUpgrade authenticates a connection with the API server and upgrades it.
func (c *Handler) serveUpgrade(writer http.ResponseWriter, request *http.Request, address string) {
	responseBytes, err := c.fetchData(http.MethodGet, c.config.APIURL+address, "")
	if err != nil {
		writer.WriteHeader(http.StatusBadGateway)
		writer.Write([]byte("bad gateway"))
		log.Printf("MethodGet: %s", err.Error())
		return
	}
	if responseBytes != "ok" {
		writer.WriteHeader(http.StatusForbidden)
		writer.Write([]byte("forbidden"))
		return
	}
	if !strings.EqualFold(request.Header.Get("Upgrade"), "websocket") {
		writer.WriteHeader(http.StatusBadRequest)
		writer.Write([]byte("no upgrade requested"))
		return
	}

	connection, err := c.upgrader.Upgrade(writer, request)
	if err != nil {
		log.Println("MethodGet: could not upgrade connection")
		return
	}
	// Claimed after the upgrade, because before it there is no connection to
	// register, and refused when the ClientId is already connected.
	if !c.claim(address, connection) {
		atomic.AddUint64(&c.statistics.connectionsDenied, 1)
		log.Printf("MethodGet: %s is already connected", address)
		connection.WriteClose(1008, []byte("already connected"))
		return
	}
	c.addresses.Store(connection, address)

	atomic.AddUint64(&conns, 1)
	atomic.AddUint64(&c.statistics.connectionsOpened, 1)
	connection.ReadLoop()
	c.release(address, connection)
	c.addresses.Delete(connection)
	atomic.AddUint64(&conns, ^uint64(0)) // decrement by one
	atomic.AddUint64(&c.statistics.connectionsClosed, 1)
}

// serveStatistics answers in the Prometheus text exposition format, which any
// OpenMetrics-compatible scraper reads. The counter names are unchanged; what
// is new is the type and help metadata a scraper needs, and active_connections,
// which the README previously told you to work out by hand.
func (c *Handler) serveStatistics(writer http.ResponseWriter) {
	counters := []struct {
		name  string
		help  string
		value *uint64
	}{
		{"connections_opened", "WebSocket connections accepted.", &c.statistics.connectionsOpened},
		{"connections_closed", "WebSocket connections that have ended.", &c.statistics.connectionsClosed},
		{"connections_denied", "Upgrades refused because the ClientId was already connected.", &c.statistics.connectionsDenied},
		{"requests_started", "Requests sent to the API server.", &c.statistics.requestsStarted},
		{"requests_failed", "Requests to the API server that failed.", &c.statistics.requestsFailed},
		{"requests_succeeded", "Requests to the API server that succeeded.", &c.statistics.requestsSucceeded},
		{"messages_dropped", "Client messages the proxy did not forward.", &c.statistics.messagesDropped},
	}
	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	for _, counter := range counters {
		fmt.Fprintf(writer, "# HELP %s %s\n", counter.name, counter.help)
		fmt.Fprintf(writer, "# TYPE %s counter\n", counter.name)
		fmt.Fprintf(writer, "%s %d\n", counter.name, atomic.LoadUint64(counter.value))
	}
	fmt.Fprintf(writer, "# HELP active_connections Connections currently open.\n")
	fmt.Fprintf(writer, "# TYPE active_connections gauge\n")
	fmt.Fprintf(writer, "active_connections %d\n", c.connections.Len())
}

// serveMemProfile writes a heap profile. It has its own path because writing a
// file is a side effect no metrics scrape should have, and a scraper polling
// the statistics endpoint every fifteen seconds would rewrite it forever.
func (c *Handler) serveMemProfile(writer http.ResponseWriter) {
	if c.config.MemProfile == "" {
		writer.WriteHeader(http.StatusNotFound)
		writer.Write([]byte("no memprofile configured"))
		return
	}
	f, err := os.Create(c.config.MemProfile)
	if err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		writer.Write([]byte("could not create profile"))
		log.Printf("memprofile: %s", err.Error())
		return
	}
	defer f.Close()
	if err := pprof.WriteHeapProfile(f); err != nil {
		writer.WriteHeader(http.StatusInternalServerError)
		writer.Write([]byte("could not write profile"))
		log.Printf("memprofile: %s", err.Error())
		return
	}
	writer.Write([]byte("ok"))
}

func (c *Handler) OnMessage(connection *gws.Conn, message *gws.Message) {
	defer message.Close()
	atomic.AddUint64(&rps, 1)
	if message.Opcode == gws.OpcodeBinary {
		// Counted rather than logged: a client sending binary in a loop would
		// otherwise be a way to fill the disk with log lines.
		atomic.AddUint64(&c.statistics.messagesDropped, 1)
		return
	}
	if message.Opcode == gws.OpcodePing {
		if err := connection.WritePong(message.Bytes()); err != nil {
			log.Println(err.Error())
		}
		return
	}
	if message.Opcode != gws.OpcodeText {
		return
	}

	address, ok := c.addresses.Load(connection)
	if !ok {
		atomic.AddUint64(&c.statistics.messagesDropped, 1)
		log.Println("OnMessage: could not find address")
		return
	}
	responseBytes, err := c.fetchData(http.MethodPost, c.config.APIURL+address, message.Data.String())
	if err != nil {
		// Deliberately not written back to the client. The body of a failed
		// request is an error page or a partial read, and forwarding it would
		// put the API's internals on the wire dressed as a reply.
		log.Println(err.Error())
		return
	}
	if responseBytes == "" {
		// A handler with nothing to say. Writing an empty frame would make
		// every client filter them out, which the protocol says it should not
		// have to do.
		return
	}
	if err := connection.WriteString(responseBytes); err != nil {
		log.Println(err.Error())
	}
}

func (c *Handler) OnClose(connection *gws.Conn, err error) {
	address, ok := c.addresses.Load(connection)
	if !ok {
		// Either this upgrade was refused as a duplicate, or the connection was
		// never registered. Either way the API has nothing to clean up, and the
		// ClientId belongs to somebody else's live connection.
		return
	}
	reason := err.Error()
	if closeErr, ok := err.(*gws.CloseError); ok {
		reason = string(closeErr.Reason)
	}
	responseBytes, err := c.fetchData(http.MethodDelete, c.config.APIURL+address, reason)
	if err != nil {
		log.Printf("OnClose: %s: %s", address, err.Error())
		return
	}
	if responseBytes != "ok" {
		log.Printf("OnClose: %s: the API did not acknowledge the disconnect", address)
	}
}
