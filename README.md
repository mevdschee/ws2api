# WS to API

Proxy messages from Websockets to RoadRunner PHP.

    WS client --[ws upgrade]--> WS server --[http get request]--> API server

    WS client <--[ws connect]-- WS server <--[http response "ok"]-- API server
    
    WS client --[message]--> WS server --[http post request]--> API server

    WS client <--[message]-- WS server <--[http response]-- API server

And also:

    API server --[http post request]--> WS server --[message]--> WS client

Note that responses to server-to-client requests are handled as client-to-server requests.

NB: Use HAproxy with Origin header and disabled Keep-Alive to go from WSS to WS.

### Websocket

Websockets send an HTTP upgrade after that they can send messages in either direction.

### Websocket upgrade

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

The websocket messages that are received are sent using a HTTP request to the server:

    POST /<ClientId>
    Host: API server
    
    <RequestMessage>

Adn the HTTP request may have a response:

    <ResponseMessage>

If the response is non-empty, then it is sent back on the (right) websocket as a message in the reverse direction.

### API to WS

A websocket message can be also be sent using a HTTP request to the websocket proxy:

    POST /<ClientId>
    Host: wsproxy
    
    <RequestMessage>

The response that the WS client may send needs to be filtered from the incomming request messages.

### Profiling

The proxy application suppports the standard "-cpuprofile=" and "-memprofile=" flags to create pprof profiles.

### Performance results

The proxy application was benchmarked to build up and hold 120k connections each doing one message per 10 seconds
in 30 seconds (from 0 to 120k connections) and with 12k messages per second within 32GB RAM.

### Warning

The roadrunner server application is currently unbound and will use as much RAM as it needs easily using up to 64GB of RAM.
Also the setup requires many open files, so you may want to set the using "ulimit -n 200000".
