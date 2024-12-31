#!/bin/bash
# sysctl -w net.netfilter.nf_conntrack_max=2621440
# echo "net.netfilter.nf_conntrack_max=2621440" >> /etc/sysctl.conf
echo 2621440 | sudo tee /proc/sys/net/netfilter/nf_conntrack_max
