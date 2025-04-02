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

Other strings are treated as error messages.

### WS to API

The websocket messages that are received are sent using a HTTP request to the
API server:

    POST /<ClientId>
    Host: API server

    <RequestMessage>

Adn the HTTP request may have a response:

    <ResponseMessage>

If the response is non-empty, then it is sent back on the (right) websocket as a
message in the reverse direction.

### API to WS

A websocket message can be also be sent using a HTTP request to the websocket
server:

    POST /<ClientId>
    Host: WS server

    <RequestMessage>

The response that the WS client may send needs to be filtered from the incomming
request messages.

### Profiling

The proxy application suppports the standard "-cpuprofile=" and "-memprofile="
flags to create pprof profiles.

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

You can let Prometheus (or another OpenMetrics compatible) scraper scrape the
metrics of the proxy. Amongst other variables it keeps track of are:

- connections_opened
- connections_closed
- requests_started
- requests_failed
- requests_succeeded

You can find the number of open connections by calculating: 

    active_connections = requests_started - (requests_succeeded + requests_failed)

You can do this within Grafana using PromQL or another query language.

### Other implementations

- Go with GWS (this repo)
- PHP with OpenSwoole ([source](https://github.com/mevdschee/ws2api-php))
- PHP with Swow ([source](https://github.com/mevdschee/ws2api-php-alt))
- JS with Deno ([source](https://github.com/mevdschee/ws2api-js))

Note that the performance of the non-Go implementations may vary.
