# ToolHive developer guide <!-- omit in toc -->

The ToolHive development documentation provides guidelines and resources for
developers working on the ToolHive project. It includes information on setting
up the development environment, contributing to the codebase, and understanding
the architecture of the project.

For user-facing documentation, please refer to the
[ToolHive docs website](https://docs.stacklok.com/toolhive/).

## Contents <!-- omit in toc -->

- [Getting started](#getting-started)
  - [Prerequisites](#prerequisites)
  - [Building ToolHive](#building-toolhive)
  - [Running tests](#running-tests)
  - [Other development tasks](#other-development-tasks)
- [Contributing](#contributing)

Explore the contents of this directory to find more detailed information on
specific topics related to ToolHive development including architectural details
and [design proposals](./proposals).

For information on the ToolHive Operator, see the
[ToolHive Operator README](../cmd/thv-operator/README.md) and
[DESIGN doc](../cmd/thv-operator/DESIGN.md).

## Getting started

ToolHive is developed in Go. To get started with development, you need to
install Go and set up your development environment.

### Prerequisites

- **Go**: ToolHive requires Go 1.24. You can download and install Go from the
  [official Go website](https://go.dev/doc/install).

- **Task** (Recommended): Install the [Task](https://taskfile.dev/) tool to run
  automated development tasks. You can install it using Homebrew on macOS:

  ```bash
  brew install go-task
  ```

### Building ToolHive

To build the ToolHive CLI (`thv`), follow these steps:

1. **Clone the repository**: Clone the ToolHive repository to your local machine
   using Git:

   ```bash
   git clone https://github.com/stacklok/toolhive.git
   cd toolhive
   ```

2. **Build the project**: Use the `task` command to build the binary:

   ```bash
   task build
   ```

3. **Run ToolHive**: The build task creates the `thv` binary in the `./bin/`
   directory. You can run it directly from there:

   ```bash
   ./bin/thv
   ```

4. Optionally, install the `thv` binary in your `GOPATH/bin` directory:

   ```bash
   task install
   ```

### Running tests

To run the linting and unit tests for ToolHive, run:

```bash
task lint
task test
```

ToolHive also includes comprehensive end-to-end tests that can be run using:

```bash
task test-e2e
```

### Other development tasks

To see a list of all available development tasks, run:

```bash
task --list
```

## Contributing

We welcome contributions to ToolHive! If you want to contribute, please review
the [contributing guide](../CONTRIBUTING.md).

Contributions to the user-facing documentation are also welcome. If you have
suggestions or improvements, please open an issue or submit a pull request in
the [docs-website repository](https://github.com/stacklok/docs-website).
