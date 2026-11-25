# Builds the MCP conformance reference server from the conformance repo's
# examples/servers/typescript directory. This Dockerfile is copied into that
# directory by run-conformance.sh before `docker build`, so the build context
# is the server source (package.json, *.ts).
#
# The stock `npm start` (tsx) entrypoint is used unchanged: it works behind
# `thv run` because the CLI workload container is not read-only-rootfs.
FROM node:22-slim
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY *.ts ./
ENV PORT=3000
EXPOSE 3000
CMD ["npm", "start"]
