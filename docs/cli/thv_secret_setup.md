## thv secret setup

Set up secrets provider

### Synopsis

Interactive setup for configuring a secrets provider.
This command will guide you through selecting and configuring
a secrets provider for storing and retrieving secrets.

Available providers:
  - encrypted: Stores secrets in an encrypted file using AES-256-GCM using the OS Keyring
  - 1password: Read-only access to 1Password secrets (requires OP_SERVICE_ACCOUNT_TOKEN)
  - none: Disables secrets functionality

You must run this command before using any other secrets functionality.

```
thv secret setup [flags]
```

### Options

```
  -h, --help   help for setup
```

### Options inherited from parent commands

```
      --debug   Enable debug mode
```

### SEE ALSO

* [thv secret](thv_secret.md)	 - Manage secrets

