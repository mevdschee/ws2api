# WS2API

Proxy messages from Websockets to RoadRunner PHP. Use HAproxy with Origin header and disabled Keep-Alive to go from WSS to WS.

### Websocket RPC framework

Websocket messages can follow the WAMP protocol (see: https://wamp-proto.org/index.html) and especially the RPC implementation as described here:

https://wamp-proto.org/wamp_latest_ietf.html#name-calling-and-invocations

We implement the simplified RPC part of the WAMP protocol as specified in the OCPP protocol here:

https://openchargealliance.org/my-oca/ocpp/

Specifically look for chapter 4 "RPC Framework" as defined in the JSON specification.

### Websocket to REST

We convert RPC calls (from 4.2.1) to the above RPC specification to REST calls

    GET /<ClientId> HTTP/1.1
    Upgrade: websocket
    Connection: Upgrade

And after the websocket is upgraded, the syntax of a CALL looks like this:

    [<MessageTypeId>, "<MessageId>", "<Action>", {<Payload>}]

And the result looks like this

    [<MessageTypeId>, "<MessageId>", {<Payload>}]

In case of an error it is:

    [<MessageTypeId>, "<MessageId>", "<errorCode>", "<errorDescription>", {<errorDetails>}]

It is converted to a REST call with the following content:

    POST /<Action>/<ClientId>/<MessageId>\
    Content-Type: application/json
    
    {<Payload>}

And the JSON Payload of the result is in the body of the HTTP response.

    Content-Type: application/json
    
    {<Payload>}

### Rest to Websocket

WIP
