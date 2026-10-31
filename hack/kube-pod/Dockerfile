# Only the static terminal HTML is copied from cloudtty; its server is not used.
FROM ghcr.io/cloudtty/cloudshell:v0.8.5@sha256:36bcf73b3c6896fd001e80f36c72da975e64de5509526a406c133dfcf84dab3c AS cloudshell
# Keep ZMODEM file transfer on Alpine with the maintained, security-fixed source
# and Debian's receive-buffer fixes. Verify both publisher source archives.
FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a AS lrzsz-builder
RUN apk add --no-cache build-base curl ca-certificates patch bc
WORKDIR /build
RUN curl -fsSL https://deb.debian.org/debian/pool/main/l/lrzsz/lrzsz_0.13.1.orig.tar.gz -o source.tar.gz \
    && curl -fsSL https://deb.debian.org/debian/pool/main/l/lrzsz/lrzsz_0.13.1-1.debian.tar.xz -o patches.tar.xz \
    && printf '%s\n' \
       '112f91059fbd118e016d800c1a5c5a1702e965d8b9bfbda6015e44e20bfb93c9  source.tar.gz' \
       '0cede4418e5c3a2a0043f74913564fa366812c7479764c33351db7e08d762a8f  patches.tar.xz' \
       | sha256sum -c - \
    && mkdir source \
    && tar -xzf source.tar.gz -C source --strip-components=1 \
    && tar -xf patches.tar.xz -C source
WORKDIR /build/source
RUN while read -r name; do patch -p1 < "debian/patches/$name"; done < debian/patches/series \
    && ./configure --disable-nls --disable-rpath --disable-Werror --program-transform-name=s/l// --prefix=/usr/local \
    && make -j2 \
    && make DESTDIR=/out install
RUN --network=none make quickcheck

FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a AS base

RUN test "$(apk --print-arch)" = x86_64 && \
    apk upgrade --no-cache && \
    apk add --no-cache \
    'curl>=8.23.0-r0' \
    'libcurl>=8.23.0-r0' \
    'brotli-libs>=1.2.1-r0' \
    gnupg \
    ca-certificates \
    git \
    tmux \
    screen \
    jq \
    openssl \
    gettext-envsubst \
    iputils \
    net-tools \
    openssh-client \
    bash \
    bash-completion \
    zsh \
    fish \
    vim \
    less \
    htop \
    bind-tools \
    netcat-openbsd \
    procps \
    coreutils \
    util-linux

# Build final image
FROM base AS final
COPY --from=lrzsz-builder /out/usr/local/bin/ /usr/local/bin/
COPY --from=lrzsz-builder /build/source/COPYING /usr/share/licenses/lrzsz/COPYING
COPY --from=lrzsz-builder /build/source/debian/copyright /usr/share/licenses/lrzsz/debian-copyright

# Create necessary directories
RUN mkdir -p /usr/local/etc /scripts /root/.kube /usr/lib/ttyd \
    /etc/bash_completion.d /usr/share/zsh/site-functions \
    /root/.config/fish/completions

# Prepared from checksummed releases and the committed Go dependency locks.
COPY tools/runtime/ /usr/local/bin/
RUN cd /usr/local/bin && sha256sum -c checksums.sha256 && rm checksums.sha256

# Generate shell completions for all tools
RUN kubectl completion bash > /etc/bash_completion.d/kubectl && \
    kubectl completion zsh > /usr/share/zsh/site-functions/_kubectl && \
    kubectl completion fish > /root/.config/fish/completions/kubectl.fish && \
    kube-dc completion bash > /etc/bash_completion.d/kube-dc && \
    kube-dc completion zsh > /usr/share/zsh/site-functions/_kube-dc && \
    kube-dc completion fish > /root/.config/fish/completions/kube-dc.fish && \
    helm completion bash > /etc/bash_completion.d/helm && \
    helm completion zsh > /usr/share/zsh/site-functions/_helm && \
    helm completion fish > /root/.config/fish/completions/helm.fish && \
    stern --completion bash > /etc/bash_completion.d/stern && \
    stern --completion zsh > /usr/share/zsh/site-functions/_stern && \
    stern --completion fish > /root/.config/fish/completions/stern.fish && \
    virtctl completion bash > /etc/bash_completion.d/virtctl && \
    virtctl completion zsh > /usr/share/zsh/site-functions/_virtctl && \
    virtctl completion fish > /root/.config/fish/completions/virtctl.fish && \
    echo 'complete -o default -F __start_kubectl k' >> /etc/bash_completion.d/kubectl

# Copy index.html from cloudshell image
COPY --from=cloudshell /usr/lib/ttyd/index.html /usr/lib/ttyd/index.html

# Copy all scripts at once to reduce layers
COPY ./script/startup.sh  /scripts/
COPY ./script/kubeconfig.tmpl /usr/local/etc/kubeconfig.tmpl
COPY ./script/.bashrc /root/.bashrc
COPY ./script/kubectl_console /usr/local/bin/kubectl_console
COPY ./script/ssh_console /usr/local/bin/ssh_console

# Make scripts executable after they're copied
RUN chmod +x /scripts/*.sh /usr/local/bin/kubectl_console /usr/local/bin/ssh_console

# Set default environment variables
ENV KUBECONFIG=/root/.kube/config \
    CLIENT_ID="kube-dc" \
    HOME=/root \
    USER=root \
    LOGNAME=root

USER root
WORKDIR /root

ENTRYPOINT ["/scripts/startup.sh"]
