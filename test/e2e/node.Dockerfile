# A disposable SynCloud node for end-to-end tests: Docker-in-Docker plus the
# tools the agent needs. Nodes are privileged containers on an isolated network,
# so tests never touch the host's networking.
FROM docker:29-dind
RUN apk add --no-cache nftables curl iproute2 git git-daemon openssl
