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
	"strconv"
	"strings"
	"time"

	"github.com/mevdschee/php-observability/metrics"
)

func init() {
	runtime.GOMAXPROCS(8)
}

var cpuprofile = flag.String("cpuprofile", "", "write cpu profile to file")

var memprofile = flag.String("memprofile", "", "write mem profile to file")

type Handler struct {
	metrics *metrics.Metrics
}

func (c *Handler) proxyPass(writer http.ResponseWriter, request *http.Request) {
	parts := strings.SplitN(request.URL.Path, "/", 3)
	remoteHost := parts[1]
	if len(remoteHost) == 0 {
		c.metrics.Write(&writer)
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
	start := time.Now()
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(u)
			r.Out.Host = remoteHost
		},
		ModifyResponse: func(r *http.Response) error {
			c.metrics.Add("webproxy_request_responses_"+strconv.Itoa(r.StatusCode/100)+"XX", "remoteHost", remoteHost, time.Since(start).Seconds())
			return nil
		},
		ErrorHandler: func(writer http.ResponseWriter, request *http.Request, err error) {
			c.metrics.Add("webproxy_request_errors", "remoteHost", remoteHost, time.Since(start).Seconds())
			log.Println("could not proxy request: " + err.Error())
		},
	}
	c.metrics.Inc("webproxy_requests_started", "remoteHost", remoteHost, 1)
	proxy.ServeHTTP(writer, request)
	c.metrics.Inc("webproxy_requests_finished", "remoteHost", remoteHost, 1)
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
		metrics: metrics.New(),
	}
	http.HandleFunc("/", handler.proxyPass)
	log.Fatal(http.ListenAndServe(":8080", nil))
}
