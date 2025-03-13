#!/usr/bin/env node
import { Server } from '@modelcontextprotocol/sdk/server/index.js';
import { StdioServerTransport } from '@modelcontextprotocol/sdk/server/stdio.js';
import {
  CallToolRequestSchema,
  ErrorCode,
  ListResourcesRequestSchema,
  ListResourceTemplatesRequestSchema,
  ListToolsRequestSchema,
  McpError,
  ReadResourceRequestSchema,
} from '@modelcontextprotocol/sdk/types.js';
import axios from 'axios';

// Get API key from environment variable
const API_KEY = process.env.OPENWEATHER_API_KEY;
if (!API_KEY) {
  console.error('OPENWEATHER_API_KEY environment variable is required');
  process.exit(1);
}

// OpenWeather API response interface
const isValidForecastArgs = (args) =>
  typeof args === 'object' &&
  args !== null &&
  typeof args.city === 'string' &&
  (args.days === undefined || typeof args.days === 'number');

class WeatherMcpServer {
  constructor() {
    this.server = new Server(
      {
        name: 'weather-mcp-server',
        version: '1.0.0',
      },
      {
        capabilities: {
          resources: {},
          tools: {},
        },
      }
    );

    this.axiosInstance = axios.create({
      baseURL: 'https://api.openweathermap.org/data/2.5',
      params: {
        appid: API_KEY,
        units: 'metric',
      },
    });

    this.setupResourceHandlers();
    this.setupToolHandlers();
    
    // Error handling
    this.server.onerror = (error) => console.error('[MCP Error]', error);
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
          uri: `weather://San Francisco/current`,
          name: `Current weather in San Francisco`,
          mimeType: 'application/json',
          description: 'Real-time weather data for San Francisco',
        },
      ],
    }));

    // List resource templates
    this.server.setRequestHandler(ListResourceTemplatesRequestSchema, async () => ({
      resourceTemplates: [
        {
          uriTemplate: 'weather://{city}/current',
          name: 'Current weather for a given city',
          mimeType: 'application/json',
          description: 'Real-time weather data for a specified city',
        },
      ],
    }));

    // Read a resource
    this.server.setRequestHandler(ReadResourceRequestSchema, async (request) => {
      const match = request.params.uri.match(/^weather:\/\/([^/]+)\/current$/);
      if (!match) {
        throw new McpError(ErrorCode.InvalidRequest, `Invalid URI format: ${request.params.uri}`);
      }
      
      const city = decodeURIComponent(match[1]);

      try {
        const response = await this.axiosInstance.get('weather', {
          params: { q: city },
        });

        return {
          contents: [
            {
              uri: request.params.uri,
              mimeType: 'application/json',
              text: JSON.stringify(
                {
                  temperature: response.data.main.temp,
                  conditions: response.data.weather[0].description,
                  humidity: response.data.main.humidity,
                  wind_speed: response.data.wind.speed,
                  timestamp: new Date().toISOString(),
                },
                null,
                2
              ),
            },
          ],
        };
      } catch (error) {
        if (axios.isAxiosError(error)) {
          throw new McpError(
            ErrorCode.InternalError,
            `Weather API error: ${error.response?.data.message ?? error.message}`
          );
        }
        throw error;
      }
    });
  }

  setupToolHandlers() {
    // List available tools
    this.server.setRequestHandler(ListToolsRequestSchema, async () => ({
      tools: [
        {
          name: 'get_forecast',
          description: 'Get weather forecast for a city',
          inputSchema: {
            type: 'object',
            properties: {
              city: {
                type: 'string',
                description: 'City name',
              },
              days: {
                type: 'number',
                description: 'Number of days (1-5)',
                minimum: 1,
                maximum: 5,
              },
            },
            required: ['city'],
          },
        },
        {
          name: 'get_current_weather',
          description: 'Get current weather for a city',
          inputSchema: {
            type: 'object',
            properties: {
              city: {
                type: 'string',
                description: 'City name',
              },
              units: {
                type: 'string',
                description: 'Temperature units (metric, imperial, standard)',
                enum: ['metric', 'imperial', 'standard'],
              },
            },
            required: ['city'],
          },
        },
      ],
    }));

    // Handle tool calls
    this.server.setRequestHandler(CallToolRequestSchema, async (request) => {
      switch (request.params.name) {
        case 'get_forecast': {
          if (!isValidForecastArgs(request.params.arguments)) {
            throw new McpError(ErrorCode.InvalidParams, 'Invalid forecast arguments');
          }

          const city = request.params.arguments.city;
          const days = Math.min(request.params.arguments.days || 3, 5);

          try {
            const response = await this.axiosInstance.get('forecast', {
              params: {
                q: city,
                cnt: days * 8, // API returns data in 3-hour steps
              },
            });

            // Process the forecast data to make it more readable
            const processedData = response.data.list.map(item => ({
              date: item.dt_txt,
              temperature: item.main.temp,
              conditions: item.weather[0].description,
              humidity: item.main.humidity,
              wind_speed: item.wind.speed
            }));

            return {
              content: [
                {
                  type: 'text',
                  text: JSON.stringify(processedData, null, 2),
                },
              ],
            };
          } catch (error) {
            if (axios.isAxiosError(error)) {
              return {
                content: [
                  {
                    type: 'text',
                    text: `Weather API error: ${error.response?.data.message ?? error.message}`,
                  },
                ],
                isError: true,
              };
            }
            throw error;
          }
        }
        
        case 'get_current_weather': {
          const { city, units = 'metric' } = request.params.arguments;

          try {
            const response = await this.axiosInstance.get('weather', {
              params: {
                q: city,
                units: units,
              },
            });

            const data = {
              city: response.data.name,
              country: response.data.sys.country,
              temperature: response.data.main.temp,
              feels_like: response.data.main.feels_like,
              conditions: response.data.weather[0].description,
              humidity: response.data.main.humidity,
              wind_speed: response.data.wind.speed,
              timestamp: new Date().toISOString(),
            };

            return {
              content: [
                {
                  type: 'text',
                  text: JSON.stringify(data, null, 2),
                },
              ],
            };
          } catch (error) {
            if (axios.isAxiosError(error)) {
              return {
                content: [
                  {
                    type: 'text',
                    text: `Weather API error: ${error.response?.data.message ?? error.message}`,
                  },
                ],
                isError: true,
              };
            }
            throw error;
          }
        }

        default:
          throw new McpError(ErrorCode.MethodNotFound, `Unknown tool: ${request.params.name}`);
      }
    });
  }

  async run() {
    const transport = new StdioServerTransport();
    await this.server.connect(transport);
    console.error('Weather MCP server running on stdio');
  }
}

const server = new WeatherMcpServer();
server.run().catch(console.error);