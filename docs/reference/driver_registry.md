# Driver Registry Reference

dbc connects to one or more driver registries this document covers the conventions for how these registries are structured and how to use them.

To be compatible with dbc, a driver registry must:

1. Serve an `index.yaml` file with a list of drivers and driver archives. See [Registry Specification](#registry-specification).
2. Serve dbc-compatible driver archives. See [Driver Package Specification](#driver-package-specification)

By default, dbc connects to Columnar two CDNs, both of which follow this registry specification:

- [dbc-cdn.columnar.tech](https://dbc-cdn.columnar.tech)
- [dbc-cdn-private.columnar.tech](https://dbc-cdn-private.columnar.tech)

TODO: Also talk about custom registries.

Continue reading to learn how registries should be structured so dbc can connect to them.

## Registry Specification

For a registry to be compatible with dbc, it must serve an `index.yaml` at its base URL. This file lists all the available drivers, including metadata and paths to platform-specific archives.

### Minimal index.yaml

```yaml
--8<-- "testdata/registry_spec/index_minimal.yaml"
```

When `url` is omitted, packages are fetched from:

```
<base_url>/<path>/<version>/<path>_<platform>-<version>.tar.gz
```

### Recommended index.yaml

```yaml
--8<-- "testdata/registry_spec/index_recommended.yaml"
```

Relative `url` values are resolved against the registry's base URL. Absolute URLs are used as-is.

## Package Specification

Each package is a `.tar.gz` archive with no subdirectories.

### Minimal package

```
MANIFEST
```

```toml
--8<-- "testdata/registry_spec/MANIFEST_minimal.toml"
```

### Recommended package

```
MANIFEST
libmy_driver.so
libmy_driver.so.sig
```

```toml
--8<-- "testdata/registry_spec/MANIFEST_recommended.toml"
```

`dbc install` verifies `Files.signature` against Columnar's public key. Use `--no-verify` to install packages that aren't signed by Columnar.

## Setting a custom user agent

TODO

## Driver Package Specification

TODO
