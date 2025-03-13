FROM node:22-alpine

WORKDIR /app

# Copy package.json and package-lock.json
COPY package.json ./

# Install dependencies
RUN npm install

# Copy the rest of the application
COPY . .

# Make the index.js file executable
RUN chmod +x index.js

# Run the MCP server
CMD ["node", "index.js"]