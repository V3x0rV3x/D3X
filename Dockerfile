# Image de D3X en mode serveur (NAS, serveur). Elle reprend les binaires Linux
# compilés et signés par le workflow Release (dist/), sans rien recompiler.
# Alpine plutôt que scratch : le binaire mediainfo y est, pour que le lot
# calcule le MediaInfo lui-même, page fermée.
#
#   docker run -d --name d3x -p 8733:8733 -v d3x:/config -v /mes/medias:/data:ro ghcr.io/v3x0rv3x/d3x
#   docker logs d3x   # le lien d'accès à la page
FROM alpine:3.22
ARG TARGETARCH
RUN apk add --no-cache ca-certificates tzdata mediainfo \
 && mkdir -p /config /data \
 && chown 1000:1000 /config
COPY dist/d3x_linux_${TARGETARCH} /d3x
# Réglages, jeton d'accès, cache de hachage et clés SSH : tout dans /config.
ENV D3X_LISTEN=0.0.0.0:8733 \
    D3X_MANAGED=docker \
    HOME=/config \
    XDG_CONFIG_HOME=/config \
    TZ=Europe/Paris
VOLUME /config
EXPOSE 8733
USER 1000:1000
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --start-interval=2s CMD ["/d3x", "health"]
ENTRYPOINT ["/d3x"]
