#!/bin/bash
# see also: https://morganwu277.github.io/2018/05/26/Solve-production-issue-of-nf-conntrack-table-full-dropping-packet/
# see also: https://medium.com/@pawilon/tuning-your-linux-kernel-and-haproxy-instance-for-high-loads-1a2105ea553e
# sysctl -w net.netfilter.nf_conntrack_max=2621440
# echo "net.netfilter.nf_conntrack_max=2621440" >> /etc/sysctl.conf
echo 2621440 | sudo tee /proc/sys/net/netfilter/nf_conntrack_max
