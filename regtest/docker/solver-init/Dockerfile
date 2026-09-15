# Isolates @arkade-os/sdk in an image so the host orchestrator stays dependency-free.
FROM node:20-alpine

WORKDIR /app

COPY package.json ./
RUN npm install --omit=dev

COPY index.mjs ./

ENTRYPOINT ["node", "index.mjs"]
