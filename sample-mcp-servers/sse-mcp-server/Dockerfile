FROM node:22-alpine

WORKDIR /app

# Install TypeScript globally
RUN npm install -g typescript

# Copy source files
COPY *.ts ./
COPY tsconfig.json ./
COPY package.json ./

# Install dependencies without running scripts
RUN npm install --ignore-scripts

# Build TypeScript files
RUN tsc

ENV NODE_ENV=production
ENV PORT=3000

EXPOSE 3000

# Run the SSE server
CMD ["node", "dist/sse.js"]