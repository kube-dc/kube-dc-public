FROM quay.io/frrouting/frr:10.4.5@sha256:b0faf7c8f3d8b09aea1e38f77603c745a66dbf5e26ef9718527c2fbb53cc3aed AS frr

# FRR 10.4 uses libyang ABI 2. Keep that ABI and backport the upstream LYB
# overflow checks, rather than changing the routing engine's major version.
FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a AS libyang
RUN apk add --no-cache build-base cmake samurai pcre2-dev cmocka-dev curl ca-certificates patch bash
WORKDIR /build
RUN curl -fsSL https://github.com/CESNET/libyang/archive/v2.1.148.tar.gz -o source.tar.gz \
    && echo '77a0aaaeb3df720aeb70d6896e32e2c2be099d48df73e3cfb52567051af3e44b  source.tar.gz' | sha256sum -c - \
    && tar -xzf source.tar.gz --strip-components=1
COPY libyang-overflow.patch /build/libyang-overflow.patch
RUN patch -F0 -p1 < libyang-overflow.patch \
    && cmake -B build -G Ninja -DCMAKE_BUILD_TYPE=MinSizeRel -DCMAKE_POLICY_VERSION_MINIMUM=3.5 \
       -DCMAKE_INSTALL_PREFIX=/usr -DCMAKE_INSTALL_LIBDIR=lib \
       -DBUILD_SHARED_LIBS=ON -DENABLE_TESTS=ON \
    && cmake --build build --parallel 2
# Match Alpine's noninteractive library test selection, including LYB parser
# regression tests for valid data and both overflowing wire-length fields.
RUN --network=none ctest --test-dir build --output-on-failure -E 'yanglint.*'
RUN DESTDIR=/out cmake --install build

FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a

USER root
LABEL io.kube-dc.frr.version="10.4.5" \
      io.kube-dc.frr.upstream-image="quay.io/frrouting/frr:10.4.5@sha256:b0faf7c8f3d8b09aea1e38f77603c745a66dbf5e26ef9718527c2fbb53cc3aed" \
      io.kube-dc.libyang.version="2.1.148+kube-dc.lyb-overflow.1" \
      io.kube-dc.libyang.source-sha256="77a0aaaeb3df720aeb70d6896e32e2c2be099d48df73e3cfb52567051af3e44b"

# The runtime drops CAP_CHOWN and CAP_DAC_OVERRIDE. Make root the owner at
# build time and inherit group frr on the generated configuration instead.
RUN apk upgrade --no-cache \
    && apk add --no-cache nftables libcap-utils iproute2 json-c pcre2 lua5.3-libs protobuf-c readline python3 tini \
    && addgroup -g 101 frr \
    && addgroup -g 102 frrvty \
    && adduser -D -H -u 100 -G frr -s /sbin/nologin -h /run/frr frr \
    && addgroup frr frrvty \
    && mkdir -p /etc/frr /run/frr /usr/lib/frr/modules /usr/share/licenses/libyang \
    && install -o root -g frr -m 0640 /dev/null /etc/frr/frr.conf \
    && install -o root -g frr -m 0640 /dev/null /etc/frr/vtysh.conf \
    && chown root:frr /etc/frr /run/frr \
    && chmod 2770 /etc/frr /run/frr

# The gateway uses zebra, bgpd and vtysh only. Do not inherit upstream debug
# packages, headers or unused protocol/RPKI modules and their SSH dependencies.
COPY --from=frr /usr/lib/frr/bgpd /usr/lib/frr/zebra /usr/lib/frr/
COPY --from=frr /usr/bin/vtysh /usr/bin/vtysh
COPY --from=frr /usr/lib/libfrr.so.0* /usr/lib/libmlag_pb.so.0* /usr/lib/
COPY --from=frr /usr/share/yang/ /usr/share/yang/
COPY --from=libyang /out/usr/lib/libyang.so* /usr/lib/
COPY --from=libyang /build/LICENSE /usr/share/licenses/libyang/LICENSE
COPY --from=libyang /build/libyang-overflow.patch /usr/share/licenses/libyang/kube-dc-overflow.patch
COPY runtime-sources.json /usr/share/kube-dc/routing-runtime-sources.json

# Verify the composed runtime itself: the upstream programs must resolve every
# library, keep libyang ABI 2 and execute without network access. libmlag_pb
# deliberately obtains symbols from zebra's libfrr/protobuf dependencies, so
# check it through zebra's complete load graph rather than as a standalone app.
RUN --network=none set -eu; \
    for binary in /usr/lib/frr/bgpd /usr/lib/frr/zebra /usr/bin/vtysh /usr/lib/libfrr.so.0; do \
      dependencies="$(ldd "$binary" 2>&1)"; \
      printf '%s\n' "$dependencies"; \
      if printf '%s\n' "$dependencies" | grep -E 'not found|Error loading|Error relocating'; then exit 1; fi; \
    done; \
    for binary in /usr/lib/frr/bgpd /usr/lib/frr/zebra /usr/bin/vtysh /usr/lib/libfrr.so.0; do \
      test "$(ldd "$binary" | awk '/libyang/ { print $1 }')" = libyang.so.2; \
    done; \
    LD_BIND_NOW=1 /usr/lib/frr/bgpd --version; \
    LD_BIND_NOW=1 /usr/lib/frr/zebra --version; \
    LD_BIND_NOW=1 /usr/bin/vtysh --help; \
    sha256sum /usr/lib/frr/bgpd /usr/lib/frr/zebra /usr/bin/vtysh \
      /usr/lib/libfrr.so.0 /usr/lib/libmlag_pb.so.0 /usr/lib/libyang.so.2 \
      /usr/share/licenses/libyang/kube-dc-overflow.patch \
      > /usr/share/kube-dc/routing-runtime.sha256

COPY entrypoint.sh /usr/local/bin/kube-dc-routing-entrypoint
COPY exporter.py /usr/local/bin/kube-dc-routing-exporter
COPY nft-counter-collector.sh /usr/local/bin/kube-dc-nft-counter-collector
COPY render_config.py /usr/local/bin/kube-dc-render-frr-config

RUN chmod 0755 \
      /usr/local/bin/kube-dc-routing-entrypoint \
      /usr/local/bin/kube-dc-routing-exporter \
      /usr/local/bin/kube-dc-nft-counter-collector \
      /usr/local/bin/kube-dc-render-frr-config

EXPOSE 9100

ENTRYPOINT ["/sbin/tini", "-g", "--", "/usr/local/bin/kube-dc-routing-entrypoint"]
