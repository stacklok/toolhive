Feature: MCP Server Lifecycle
  As a user of mcp-lok
  I want to manage MCP server lifecycles
  So that I can start, list, stop, and remove MCP servers

  @server
  Scenario: Starting and stopping an MCP server
    Given I have a valid MCP server image
    When I start an MCP server with name "test-server" and transport "stdio"
    Then the server should be running
    And I should see the server in the list of running servers
    When I stop the server
    Then the server should not be running