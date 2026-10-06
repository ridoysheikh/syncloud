#!/usr/bin/env bash
# End-to-end test of the private network (§8): WireGuard mesh, cross-node
# container traffic without NAT, egress, host firewall and node removal.
#
#   test/e2e/mesh.sh          # run and clean up
#   KEEP=1 test/e2e/mesh.sh   # leave the nodes running for inspection
#
# The containers here are plain docker run containers, not SynCloud tasks, so
# security groups (which only admit known tasks) are off; secgroups.sh
# covers them.
CTL_FLAGS="--security-groups=false ${CTL_FLAGS:-}"
. "$(dirname "$0")/lib.sh"
trap cleanup EXIT
setup_cluster
wait_mesh
m=$(mesh)
echo "$m" | grep -o '"name":"[a-z0-9-]*","address":"[0-9.]*","subnet":"[0-9./]*"' | sed 's/"//g'
echo "$m" | grep -o '"mode":"[a-z]*"' | sort | uniq -c

echo "== cross-node container traffic"
# A web container on w1 whose CGI echoes the caller's address.
CGI='mkdir -p /www/cgi-bin && printf "#!/bin/sh\necho Content-Type: text/plain\necho\necho \$REMOTE_ADDR\n" > /www/cgi-bin/ip && chmod +x /www/cgi-bin/ip && exec httpd -f -p 8080 -h /www'
x sc-e2e-w1 docker run -d --name web --network syncloud busybox:1.37 sh -c "$CGI" >/dev/null
W1_IP=$(x sc-e2e-w1 docker inspect -f '{{.NetworkSettings.Networks.syncloud.IPAddress}}' web)
echo "web on w1: $W1_IP"
x sc-e2e-w2 docker run -d --name client --network syncloud busybox:1.37 sleep 300 >/dev/null
W2C=$(x sc-e2e-w2 docker inspect -f '{{.NetworkSettings.Networks.syncloud.IPAddress}}' client)
sleep 1
seen=$(x sc-e2e-w2 docker exec client wget -q -T 5 -O - "http://$W1_IP:8080/cgi-bin/ip") || fail "w2 container -> w1 container"
seen=$(echo "$seen" | tr -d '[]' | sed 's/^::ffff://')
[ "$seen" = "$W2C" ] || fail "source address rewritten: w1 saw $seen, client is $W2C"
echo "  ✓ container on w2 ($W2C) -> container on w1, source address preserved"
x sc-e2e-ctl wget -q -T 5 -O /dev/null "http://$W1_IP:8080/cgi-bin/ip" || fail "controller host -> w1 container"
echo "  ✓ controller host (Traefik's view) -> container on w1"
x sc-e2e-w2 docker exec client wget -q -T 8 -O /dev/null http://example.com || fail "egress"
echo "  ✓ container egress to the internet (masqueraded)"
if x sc-e2e-w2 wget -q -T 3 -O /dev/null "http://$W1_IP:8080/cgi-bin/ip" 2>/dev/null; then
  echo "  ✓ worker host -> remote container (mesh)"
fi

echo "== host firewall"
W1_PUB=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-w1)
W2_PUB=$(docker inspect -f "{{(index .NetworkSettings.Networks \"$NET\").IPAddress}}" sc-e2e-w2)
x -d sc-e2e-w1 sh -c 'while true; do echo hello | nc -l -p 2222; done'
sleep 1
probe() { x sc-e2e-w2 sh -c "echo | timeout 3 nc $W1_PUB 2222 2>/dev/null" | grep -q hello; }
if probe; then fail "port 2222 on w1 is reachable without a rule"; fi
echo "  ✓ default deny: w2 cannot reach w1:2222 on its public address"
api localhost:7070/api/v1/firewall/policies -d "{\"name\":\"e2e\",\"rules\":[{\"protocol\":\"tcp\",\"ports\":\"2222\",\"sources\":[\"$W2_PUB\"]}]}" >/dev/null
for _ in $(seq 1 15); do probe && break; sleep 1; done
probe || fail "allow rule not applied"
echo "  ✓ policy allows w2 -> w1:2222"
x sc-e2e-w1 docker run --rm --network syncloud busybox:1.37 true
x sc-e2e-w2 docker exec client wget -q -T 5 -O /dev/null "http://$W1_IP:8080/cgi-bin/ip" || fail "mesh traffic blocked by the firewall"
echo "  ✓ mesh traffic still flows with the firewall on"
for _ in $(seq 1 15); do mesh | grep -q '"firewallPending":true' || break; sleep 1; done
mesh | grep -q '"firewallPending":true' && fail "firewall changes not confirmed"
echo "  ✓ firewall changes confirmed by the controller (commit-confirm)"
x sc-e2e-w1 nft flush chain inet syncloud input
probe || fail "flushing the chain should have opened the port"
for _ in $(seq 1 40); do mesh | grep -o '"name":"w1"[^}]*"driftCorrections":[1-9]' >/dev/null && break; sleep 1; done
probe && ! x sc-e2e-w1 nft list chain inet syncloud input | grep -q "default deny" && fail "drift not corrected"
x sc-e2e-w1 nft list chain inet syncloud input | grep -q "default deny" || fail "drift not corrected"
echo "  ✓ a hand-flushed ruleset is detected and restored"

echo "== node removal"
W2_ID=$(x sc-e2e-ctl curl -fs -b /tmp/jar localhost:7070/api/v1/nodes | grep -o '"id":"node_[a-z0-9]*","name":"w2"' | cut -d'"' -f4)
x sc-e2e-ctl curl -fs -b /tmp/jar -X DELETE "localhost:7070/api/v1/nodes/$W2_ID" -H 'Origin: http://localhost:7070' >/dev/null
sleep 3
m=$(mesh); echo "$m" | grep -q '"name":"w2"' && fail "w2 still in mesh"
echo "  ✓ w2 removed from the mesh"
echo "PASS"
