#!/bin/bash
echo 2621440 | sudo tee /proc/sys/net/netfilter/nf_conntrack_max
