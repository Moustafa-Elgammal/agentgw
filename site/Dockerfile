# Node toolchain for the project site. Runs only in containers; nothing is installed on the host.
# The repository is mounted at /repo by docker-compose.yml, node_modules lives in a named volume.
FROM node:22-slim
ENV ASTRO_TELEMETRY_DISABLED=1 \
    NPM_CONFIG_UPDATE_NOTIFIER=false \
    NPM_CONFIG_FUND=false \
    NPM_CONFIG_AUDIT=false \
    REPO_ROOT=/repo
RUN apt-get update && apt-get install -y --no-install-recommends fontconfig fonts-dejavu-core \
 && rm -rf /var/lib/apt/lists/*
COPY entrypoint.sh /usr/local/bin/site-entrypoint
RUN chmod +x /usr/local/bin/site-entrypoint
WORKDIR /repo/site
ENTRYPOINT ["site-entrypoint"]
CMD ["build"]
