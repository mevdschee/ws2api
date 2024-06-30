package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

func init() {
	runtime.GOMAXPROCS(8)
}

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")

var memprofile = flag.String("memprofile", "", "write mem profile to file")

type Statistics struct {
	mutex     sync.Mutex
	counters  map[string]uint64
	durations map[string]float64
}

func (s *Statistics) inc(name string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.counters[name]++
}

func (s *Statistics) add(name string, val float64) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	s.durations[name] += val
}

type Handler struct {
	statistics Statistics
}

func (c *Handler) proxyPass(writer http.ResponseWriter, request *http.Request) {
	parts := strings.SplitN(request.URL.Path, "/", 3)
	remoteHost := parts[1]
	if len(remoteHost) == 0 {
		var keys []string
		for key := range c.statistics.counters {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := c.statistics.counters[k]
			writer.Write([]byte(k + " " + strconv.FormatUint(v, 10) + "\n"))
		}
		keys = []string{}
		for key := range c.statistics.durations {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := c.statistics.durations[k]
			writer.Write([]byte(k + " " + strconv.FormatFloat(v, 'f', 3, 64) + "\n"))
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
	if len(parts) < 3 {
		log.Println("malformed url: " + request.URL.Path)
		return
	}
	remotePath := "/" + parts[2]
	remoteScheme := "https://"
	if strings.Contains(remoteHost, "localhost") {
		remoteScheme = "http://"
	}
	remoteUrl := remoteScheme + remoteHost + remotePath
	u, err := url.Parse(remoteUrl)
	if err != nil {
		log.Println("could not parse url: " + err.Error())
	}
	request.URL = u
	if err != nil {
		log.Println("could not execute request: " + err.Error())
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = remoteHost
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			c.statistics.inc("webproxy_errors{remoteHost=\"" + remoteHost + "\"}")
			log.Println("proxy error: " + err.Error())
		},
	}
	c.statistics.inc("webproxy_requests{remoteHost=\"" + remoteHost + "\"}")
	start := time.Now()
	proxy.ServeHTTP(writer, request)
	c.statistics.add("webproxy_requests_duration{remoteHost=\""+remoteHost+"\"}", time.Since(start).Seconds())
	c.statistics.inc("webproxy_requests_finished{remoteHost=\"" + remoteHost + "\"}")
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
	// start server
	handler := Handler{
		statistics: Statistics{
			counters:  map[string]uint64{},
			durations: map[string]float64{},
		},
	}
	http.HandleFunc("/", handler.proxyPass)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
