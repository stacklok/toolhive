## thv secret set

Set a secret

### Synopsis

Set a secret with the given name.

Input Methods:
		- Piped Input: If data is piped to the command, the secret value will be read from stdin.
		  Examples:
		    echo "my-secret-value" | thv secret set my-secret
		    cat secret-file.txt | thv secret set my-secret
		
		- Interactive Input: If no data is piped, you will be prompted to enter the secret value securely
		  (input will be hidden).
		  Example:
		    thv secret set my-secret
		    Enter secret value (input will be hidden): _

The secret will be stored securely using the configured secrets provider.

```
thv secret set <name> [flags]
```

### Options

```
  -h, --help   help for set
```

### Options inherited from parent commands

```
      --debug   Enable debug mode
```

### SEE ALSO

* [thv secret](thv_secret.md)	 - Manage secrets

