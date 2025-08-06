ARG CLOUDSHELL_VERSION=v0.8.5
ARG KUBECTL_VERSION=v1.32.0
ARG STERN_VERSION=1.32.0
ARG VIRTCTL_VERSION=v1.6.0
ARG TTYD_VERSION=1.7.8

# Add a named builder stage for cloudshell
FROM ghcr.io/cloudtty/cloudshell:${CLOUDSHELL_VERSION} AS cloudshell
FROM debian:12.10-slim AS base

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
    curl \
    gnupg \
    apt-transport-https \
    ca-certificates \
    lrzsz \
    git \
    tmux \
    screen \
    jq \
    openssl \
    gettext-base \
    mc \
    wget \
    iputils-ping \
    net-tools \
    openssh-client && \
    apt-get clean && \
    rm -rf /var/lib/apt/lists/*

# Build final image
FROM base AS final

# Redefine ARG to make it available in this build stage
ARG KUBECTL_VERSION
ARG STERN_VERSION
ARG VIRTCTL_VERSION
ARG TTYD_VERSION

# Create necessary directories
RUN mkdir -p /usr/local/etc /scripts /root/.kube /usr/lib/ttyd

# Install all binaries in a single layer to reduce size
RUN set -e && \
    curl -L -o /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl" && \
    chmod +x /usr/local/bin/kubectl && \
    curl -fsSL -o get_helm.sh https://raw.githubusercontent.com/helm/helm/main/scripts/get-helm-3 && \
    chmod 700 get_helm.sh && \
    ./get_helm.sh --no-sudo && \
    rm get_helm.sh && \
    curl -LO https://github.com/stern/stern/releases/download/v${STERN_VERSION}/stern_${STERN_VERSION}_linux_amd64.tar.gz && \
    tar -xvf stern_${STERN_VERSION}_linux_amd64.tar.gz && \
    mv stern /usr/local/bin/stern && \
    chmod +x /usr/local/bin/stern && \
    curl -L -o /usr/local/bin/virtctl https://github.com/kubevirt/kubevirt/releases/download/${VIRTCTL_VERSION}/virtctl-${VIRTCTL_VERSION}-linux-amd64 && \
    chmod +x /usr/local/bin/virtctl && \
    ttydArch="$(uname -m)" && \
    curl -L -o /usr/local/bin/ttyd https://github.com/cloudtty/ttyd/releases/download/${TTYD_VERSION}/ttyd.${ttydArch} && \
    chmod +x /usr/local/bin/ttyd

# Copy index.html from cloudshell image
COPY --from=cloudshell /usr/lib/ttyd/index.html /usr/lib/ttyd/index.html

# Copy all scripts at once to reduce layers
COPY ./script/startup.sh  /scripts/
COPY ./script/refresh_token.sh /scripts/
COPY ./script/kubens /usr/local/bin/kubens
COPY ./script/kubeconfig.tmpl /usr/local/etc/kubeconfig.tmpl
COPY ./script/.bashrc /root/.bashrc
COPY ./script/kubectl_console /usr/local/bin/kubectl_console    
COPY ./script/ssh_console /usr/local/bin/ssh_console

# Make scripts executable after they're copied
RUN chmod +x /scripts/*.sh /usr/local/bin/kubens /usr/local/bin/kubectl_console /usr/local/bin/ssh_console

# Set default environment variables
ENV KUBECONFIG=/root/.kube/config \
    CLIENT_ID="kube-dc"

USER root
WORKDIR /root

ENTRYPOINT ["/scripts/startup.sh"]
