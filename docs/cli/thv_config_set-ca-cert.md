## thv config set-ca-cert

Set the default CA certificate for container builds

### Synopsis

Set the default CA certificate file path that will be used for all container builds.
This is useful in corporate environments with TLS inspection where custom CA certificates are required.

Example:
  thv config set-ca-cert /path/to/corporate-ca.crt

```
thv config set-ca-cert <path> [flags]
```

### Options

```
  -h, --help   help for set-ca-cert
```

### Options inherited from parent commands

```
      --debug   Enable debug mode
```

### SEE ALSO

* [thv config](thv_config.md)	 - Manage application configuration

