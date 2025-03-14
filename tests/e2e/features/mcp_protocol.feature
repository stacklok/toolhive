Feature: MCP Protocol Compliance
  As a user of mcp-lok
  I want to ensure the MCP protocol is correctly implemented
  So that MCP servers can communicate properly with clients

  @protocol @compliance
  Scenario: MCP server initialization sequence
    Given I have a valid MCP server image
    When I start an MCP server with name "protocol-test" and transport "stdio"
    Then the server should be running
    And the server should respond to initialization requests
    When I stop the server
    Then the server should not be running

  @protocol @compliance
  Scenario: MCP server resource listing
    Given I have a valid MCP server image
    When I start an MCP server with name "resource-test" and transport "stdio"
    Then the server should be running
    And the server should respond to resource listing requests
    When I stop the server
    Then the server should not be running

  @protocol @edge
  Scenario: MCP server handles malformed messages
    Given I have a valid MCP server image
    When I start an MCP server with name "edge-test" and transport "stdio"
    Then the server should be running
    And the server should handle malformed messages gracefully
    When I stop the server
    Then the server should not be running