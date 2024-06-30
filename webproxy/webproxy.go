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
)

func init() {
	runtime.GOMAXPROCS(8)
}

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")

var memprofile = flag.String("memprofile", "", "write mem profile to file")

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

	remotePath := "/" + parts[2]
	remoteUrl := "https://" + remoteHost + remotePath
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
			c.statistics.increment(remoteHost + "_requests")
			r.SetURL(u)
			r.Out.Host = remoteHost
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			c.statistics.increment(remoteHost + "_errors")
			log.Println("proxy error: " + err.Error())
		},
	}
	c.statistics.increment(remoteHost + "_running_requests")
	proxy.ServeHTTP(writer, request)
	c.statistics.decrement(remoteHost + "_running_requests")
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
			counters: map[string]uint64{},
		},
	}
	http.HandleFunc("/", handler.proxyPass)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
