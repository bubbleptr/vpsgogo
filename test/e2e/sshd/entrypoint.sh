#!/bin/sh
set -eu
# Each container gets its own host keys, as each VPS does.
rm -f /etc/ssh/ssh_host_*
ssh-keygen -A
echo "root:${ROOT_PASSWORD:?}" | chpasswd
exec /usr/sbin/sshd -D -e
