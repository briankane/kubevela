# A stand-in for a GitHub-hosted ubuntu runner: Ubuntu with Docker Engine, run privileged.
FROM ubuntu:24.04
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
      docker.io docker-buildx iptables ca-certificates curl git openssl python3 perl procps netcat-openbsd jq \
    && rm -rf /var/lib/apt/lists/*
COPY entry.sh /usr/local/bin/entry.sh
ENTRYPOINT ["/usr/local/bin/entry.sh"]
