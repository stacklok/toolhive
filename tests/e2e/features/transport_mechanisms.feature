Feature: Transport Mechanisms
  As a user of mcp-lok
  I want to use different transport mechanisms
  So that I can communicate with MCP servers in various ways

  @transport @sse
  Scenario: Starting an MCP server with SSE transport
    Given I have a valid MCP server image
    When I start an MCP server with name "sse-server" and transport "sse" and port 8080
    Then the server should be running
    And I should see the server in the list of running servers
    When I stop the server
    Then the server should not be running