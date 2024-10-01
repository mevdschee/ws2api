#!/bin/bash

# can only inspect new connections

sudo tshark -i any -Y websocket.payload -E occurrence=l -T fields -e text -e ip.src -e ip.dst -e tcp.srcport -e tcp.dstport