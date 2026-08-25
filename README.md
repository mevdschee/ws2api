# WS to API

Proxy messages from Websockets to a RoadRunner PHP API server (see [blog post](https://tqdev.com/2024-scaling-to-1-million-websockets)).

    WS client --[ws upgrade]--> WS server --[http get request]--> API server

    WS client <--[ws connect]-- WS server <--[http response "ok"]-- API server

    WS client --[message]--> WS server --[http post request]--> API server

    WS client <--[message]-- WS server <--[http response]-- API server

    WS client --[ws close]--> WS server --[http delete request]--> API server

    WS client <--[ws disconnect]-- WS server <--[http response "ok"]-- API server

And also:

    API server --[http post request]--> WS server --[message]--> WS client

Note that responses to server-to-client requests are handled as client-to-server
requests.

NB: Use HAproxy with Origin header and disabled Keep-Alive to go from WSS to WS.

### Configuration

Every setting is a flag, and every flag has an environment variable so the proxy
can be configured in a container without rebuilding it.

| Flag | Environment | Default | Meaning |
| --- | --- | --- | --- |
| `-listen` | `WS2API_LISTEN` | `:7001` | Address to serve on |
| `-api` | `WS2API_URL` | `http://localhost:8000/wsoverhttp/` | API server URL the ClientId is appended to. Must end in a slash |
| `-max-body` | `WS2API_MAX_BODY` | `1048576` | Largest message or response accepted, in bytes |
| `-api-timeout` | `WS2API_TIMEOUT` | `60s` | Timeout for one call to the API server |
| `-stats-interval` | `WS2API_STATS_INTERVAL` | `0` | Log a throughput line on this interval. Zero, the default, disables it |

For example:

    wsproxy -listen :7001 -api http://api:8080/wsoverhttp/

The API URL is checked at start-up. One that does not end in a slash, or that is
not http or https, stops the proxy with an error rather than producing requests
that look like the API is broken.

### Websocket

A WebSocket (WS) can send an HTTP upgrade to the server and after that they can
send messages in either direction.

### WS upgrade

A connect from a websocket client may look like this:

    GET /<ClientId> HTTP/1.1
    Host: WS server
    Upgrade: websocket
    Connection: Upgrade

The websocket upgrade is converted to a HTTP request with the following content:

    GET /<ClientId>
    Host: API server

And the connection upgrade is made when the response to this message is:

    ok

Other strings are treated as error messages and the upgrade is refused with a
403. A ClientId that is already connected is refused too: the upgrade is
accepted and then closed with 1008, and `connections_denied` counts it. One
ClientId is one connection, so a second claiming the same id would otherwise
unregister the first when it closed.

### WS to API

The websocket messages that are received are sent using a HTTP request to the
API server:

    POST /<ClientId>
    Host: API server

    <RequestMessage>

And the HTTP request may have a response:

    <ResponseMessage>

If the response is non-empty, then it is sent back on the (right) websocket as a
message in the reverse direction. An empty response sends nothing at all, so a
handler with nothing to say costs no frame and clients do not have to filter
empty ones out.

A request that fails, whether it could not be made or answered other than 200,
sends nothing back either. The body of a failed request is an error page or a
partial read, and forwarding it would put the API's internals on the wire
dressed as an ordinary reply.

Binary frames are dropped and counted in `messages_dropped`; the wire format is
text.

### API to WS

A websocket message can be also be sent using a HTTP request to the websocket
server:

    POST /<ClientId>
    Host: WS server

    <RequestMessage>

The response that the WS client may send needs to be filtered from the incoming
request messages.

The proxy answers `ok` when the message was written to the socket, `404` when
that ClientId is not connected, `413` when the body is over `-max-body`, and
`502` when the socket was there but could not be written to. A `404` is the
ordinary way for the API to learn that a client has gone.

### Profiling

The proxy supports the standard `-cpuprofile` and `-memprofile` flags to create
pprof profiles.

A CPU profile is written for the life of the process. A heap profile is written
when you ask for one:

    wsproxy -memprofile heap.pprof &
    curl http://localhost:7001/debug/memprofile

It has its own path because writing a file is a side effect a metrics scrape
should not have; it used to be written on every request to the statistics
endpoint, so a scraper polling every fifteen seconds rewrote it forever.

### Performance results

The proxy application was benchmarked to build up and hold 250k connections each
doing one message per 10 seconds in 60 seconds (from 0 to 250k connections) ending
at 25k messages per second within 32GB RAM.

### Scaling

You can scale the application by load balancing using HAproxy with "uri"
load-balancing algorithm (with depth = 1). This will ensure that messages for
one `<ClientId>` will always end up on the same server. On Nginx you need to
use:

    hash $request_uri consistent;

in order to ensure that the `<ClientId>` will always end up on the same server.

### Tuning

If you dont't want the parallism to run completely wild you can limit the number
of HTTP connections from the proxy to the web server using the following 
configurable values in the HTTP client's Transport:

    MaxConnsPerHost:     10000, // c10k I guess
    MaxIdleConnsPerHost: 1000,  // just guessing
    Timeout:             60 * time.Second,

You may also have to set the nf_conntrack_max a little higher using:

    sudo sysctl -w net.netfilter.nf_conntrack_max=2621440

Next to that I suggest that you increase the max number of open files using:

    sudo sysctl -w fs.file-max=1073741816

Note that you may also need to change `/etc/security/limits.conf` file. Use
the 'ulimit -n' command to check the effective maximum number of open files.

Note that the performance was never tested in Docker containers. Also Docker
networking may have significant overhead. I suggest to use bare metal when possible.

### Statistics

A GET on `/` answers in the Prometheus text exposition format, which Prometheus
and any OpenMetrics compatible scraper read. Every counter carries its `# TYPE`
and `# HELP`, so a scraper knows what it is looking at.

| Metric | Type | Meaning |
| --- | --- | --- |
| `connections_opened` | counter | WebSocket connections accepted |
| `connections_closed` | counter | WebSocket connections that have ended |
| `connections_denied` | counter | Upgrades refused because the ClientId was already connected |
| `requests_started` | counter | Requests sent to the API server |
| `requests_failed` | counter | Requests to the API server that failed |
| `requests_succeeded` | counter | Requests to the API server that succeeded |
| `messages_dropped` | counter | Client messages the proxy did not forward |
| `active_connections` | gauge | Connections currently open |

`active_connections` is reported directly. It used to have to be derived, and
the formula the README gave for it,
`requests_started - (requests_succeeded + requests_failed)`, counted in-flight
API requests rather than open connections.

### Other implementations

- Go with GWS (this repo)
- PHP with OpenSwoole ([source](https://github.com/mevdschee/ws2api-php))
- PHP with Swow ([source](https://github.com/mevdschee/ws2api-php-alt))
- JS with Deno ([source](https://github.com/mevdschee/ws2api-js))

Note that the performance of the non-Go implementations may vary.
