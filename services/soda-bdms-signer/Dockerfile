# syntax=docker/dockerfile:1.7

ARG DEBIAN_TAG=bookworm-slim

# Download the two vendor binaries from the official Soda Music installer at
# build time. They are deliberately not committed to this repository.
FROM debian:${DEBIAN_TAG} AS extractor

ARG TARGETARCH
ARG SODA_VERSION=3.7.0
ARG SODA_BUILD=452316191
ARG SODA_INSTALLER_SHA256=e58578aec410e2c9c24835e92a1dc74d0fda982be1cf1003034538255baab0e4
ARG NODE_VERSION=22.19.0
ARG NODE_WIN_X64_SHA256=ea3fad0e67a991d8477d8c01344b56e69c676ccb733f065b22436994b1253f86

RUN test "${TARGETARCH}" = "amd64"
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl p7zip-full unzip \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /tmp/extract
RUN curl -fsSL --retry 3 \
      "https://lf-luna-release.qishui.com/obj/luna-release/${SODA_VERSION}/${SODA_BUILD}/SodaMusic-v${SODA_VERSION}-official-win32_x64.exe" \
      -o soda-installer.exe \
    && echo "${SODA_INSTALLER_SHA256}  soda-installer.exe" | sha256sum -c - \
    && mkdir installer app \
    && 7z x -y -oinstaller soda-installer.exe >/dev/null \
    && app_archive="$(find installer -type f -name app.7z -print -quit)" \
    && test -n "${app_archive}" \
    && 7z x -y -oapp "${app_archive}" >/dev/null \
    && test -s app/resources/app.asar.unpacked/bdms.node \
    && test -s app/resources/app.asar.unpacked/metasecml.dll

RUN curl -fsSL --retry 3 \
      "https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-win-x64.zip" \
      -o node.zip \
    && echo "${NODE_WIN_X64_SHA256}  node.zip" | sha256sum -c - \
    && unzip -q node.zip \
    && test -s "node-v${NODE_VERSION}-win-x64/node.exe"

RUN mkdir -p /out \
    && cp "node-v${NODE_VERSION}-win-x64/node.exe" /out/node.exe \
    && cp app/resources/app.asar.unpacked/bdms.node /out/bdms.node \
    && cp app/resources/app.asar.unpacked/metasecml.dll /out/metasecml.dll

FROM debian:${DEBIAN_TAG} AS runtime

ARG TARGETARCH
RUN test "${TARGETARCH}" = "amd64"
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl tini wine64 \
    && rm -rf /var/lib/apt/lists/*

RUN useradd --system --uid 10001 --create-home --home-dir /home/signer signer \
    && mkdir -p /app/native /data \
    && chown -R signer:signer /app /data /home/signer

COPY --from=extractor --chown=signer:signer /out/node.exe /opt/windows-node/node.exe
COPY --from=extractor --chown=signer:signer /out/bdms.node /app/native/bdms.node
COPY --from=extractor --chown=signer:signer /out/metasecml.dll /app/native/metasecml.dll
COPY --chown=signer:signer server.js entrypoint.sh /app/

RUN chmod 0755 /app/entrypoint.sh

ENV HOME=/data/home \
    XDG_CACHE_HOME=/data/cache \
    XDG_CONFIG_HOME=/data/config \
    NODE_SKIP_PLATFORM_CHECK=1 \
    WINEARCH=win64 \
    WINEDEBUG=-all \
    WINEDLLOVERRIDES=mscoree,mshtml= \
    WINEPREFIX=/data/wine-prefix \
    SODA_BDMS_HOST=0.0.0.0 \
    SODA_BDMS_PORT=17891 \
    SODA_VERSION=3.7.0 \
    SODA_VERSION_CODE=30700 \
    SODA_CHANNEL=official \
    SODA_BUILD_MODE=official \
    SODA_TIMEZONE=Asia/Shanghai

USER signer
VOLUME ["/data"]
EXPOSE 17891

HEALTHCHECK --interval=30s --timeout=3s --start-period=60s --retries=3 \
  CMD ["curl", "-fsS", "http://127.0.0.1:17891/healthz"]

ENTRYPOINT ["/usr/bin/tini", "--", "/app/entrypoint.sh"]
