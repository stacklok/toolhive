Feature: CLI Command Validation
  As a user of vt
  I want to use various CLI commands
  So that I can manage MCP servers effectively

  @cli
  Scenario: Help command should display usage information
    When I run the "help" command
    Then the output should contain "Usage"
    And the output should contain "Commands"
    And the output should contain "Options"

  @cli
  Scenario: Version flag should display version information
    When I run the command with arguments "--version"
    Then the output should contain the version number

  @cli
  Scenario: Invalid command should display an error
    When I run the "invalid-command" command
    Then the command should fail
    And the output should contain an error message