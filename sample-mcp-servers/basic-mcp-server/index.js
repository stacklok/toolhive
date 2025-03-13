#!/usr/bin/env node
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import {
  CallToolRequestSchema,
  ListResourcesRequestSchema,
  ListToolsRequestSchema,
  ReadResourceRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';

class BasicMcpServer {
  constructor() {
    this.server = new Server(
      {
        name: 'basic-mcp-server',
        version: '1.0.0',
      },
      {
        capabilities: {
          resources: {},
          tools: {},
        },
      }
    );

    this.setupResourceHandlers();
    this.setupToolHandlers();
    
    // Error handling - silently handle errors to avoid stdout/stderr pollution
    this.server.onerror = (error) => {
      // Errors are handled silently to avoid interfering with MCP communication
    };
    process.on('SIGINT', async () => {
      await this.server.close();
      process.exit(0);
    });
  }

  setupResourceHandlers() {
    // List available resources
    this.server.setRequestHandler(ListResourcesRequestSchema, async () => ({
      resources: [
        {
          uri: 'basic://info',
          name: 'Basic MCP Server Info',
          mimeType: 'application/json',
          description: 'Information about the Basic MCP Server',
        },
      ],
    }));

    // Read a resource
    this.server.setRequestHandler(ReadResourceRequestSchema, async (request) => {
      if (request.params.uri === 'basic://info') {
        return {
          contents: [
            {
              uri: request.params.uri,
              mimeType: 'application/json',
              text: JSON.stringify(
                {
                  name: 'Basic MCP Server',
                  version: '1.0.0',
                  description: 'A basic MCP server for testing mcp-lok',
                  timestamp: new Date().toISOString(),
                },
                null,
                2
              ),
            },
          ],
        };
      }

      throw new Error(`Unknown resource: ${request.params.uri}`);
    });
  }

  setupToolHandlers() {
    // List available tools
    this.server.setRequestHandler(ListToolsRequestSchema, async () => ({
      tools: [
        {
          name: 'echo',
          description: 'Echo the input text exactly as received',
          inputSchema: {
            type: 'object',
            properties: {
              text: {
                type: 'string',
                description: 'Text to echo',
              },
            },
            required: ['text'],
          },
        },
        {
          name: 'get_timestamp',
          description: 'Get the current timestamp',
          inputSchema: {
            type: 'object',
            properties: {
              format: {
                type: 'string',
                description: 'Timestamp format (iso, unix)',
                enum: ['iso', 'unix'],
              },
            },
          },
        },
      ],
    }));

    // Handle tool calls
    this.server.setRequestHandler(CallToolRequestSchema, async (request) => {
      switch (request.params.name) {
        case 'echo': {
          const { text } = request.params.arguments;
          return {
            content: [
              {
                type: 'text',
                text: `Echo: ${text}`,
              },
            ],
          };
        }
        case 'get_timestamp': {
          const { format = 'iso' } = request.params.arguments || {};
          const timestamp = format === 'unix' 
            ? Date.now().toString()
            : new Date().toISOString();
          
          return {
            content: [
              {
                type: 'text',
                text: `Current timestamp: ${timestamp}`,
              },
            ],
          };
        }
        default:
          throw new Error(`Unknown tool: ${request.params.name}`);
      }
    });
  }

  async run() {
    try {
      // Always use stdio transport for reliability
      const transport = new StdioServerTransport();
      await this.server.connect(transport);
    } catch (error) {
      throw error;
    }
    
    // Set up an interval to keep the event loop active (without logging)
    this.keepAliveInterval = setInterval(() => {
      // Keep the event loop active without logging
    }, 60000);
    
    // Set up a never-resolving promise to keep the server running
    return new Promise((resolve) => {
      // Add signal handlers to ensure clean shutdown
      process.on('SIGTERM', async () => {
        clearInterval(this.keepAliveInterval);
        await this.server.close();
        resolve();
      });
    });
  }
}

const server = new BasicMcpServer();
server.run().catch(error => {
  // Exit with error code on unhandled errors
  process.exit(1);
});