<!--
Copyright 2026 Columnar Technologies Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# Using a Driver List

dbc can create and manage lists of drivers using a [driver list](../concepts/driver_list.md) file.
By default, a driver list lives in `dbc.toml`, but it can also be embedded in a [`pyproject.toml`](#using-pyprojecttoml) or use a [custom filename](#using-a-custom-filename).

!!! note

    This functionality is similar to files from other tools such as Python's [`requirements.txt`](https://pip.pypa.io/en/stable/reference/requirements-file-format/).

A driver list is ideal for checking into version control alongside your project and is useful for recording not only which drivers your project needs but also the specific versions of each.

## Discovery

When you run any dbc command without an explicit `--path` flag (or when the path points to a file that doesn't exist yet), dbc automatically discovers the driver list by **walking up the directory tree** from the current working directory. At each level it checks (in order):

1. `pyproject.toml` — if it exists **and** contains a `[tool.dbc]` section
2. `dbc.toml`

The first match wins. This means you can run dbc commands from any subdirectory of your project:

```console
$ ls
pyproject.toml  src/  tests/
$ cd src/utils/
$ dbc add mysql        # finds pyproject.toml at project root
added mysql to driver list
```

If no driver list is found all the way up to the filesystem root, dbc falls back to `./dbc.toml` in the current directory (and will prompt you to run `dbc init`).

!!! note

    When you provide an explicit `--path` pointing to an existing file (e.g., `dbc add -p ./dbc.toml mysql`), that file is used directly — auto-discovery is skipped.

## Creating a Driver List

Create a driver list with `dbc init`:

```console
$ dbc init
$ ls
dbc.toml
$ cat dbc.toml
[drivers]

```

Driver lists uses the [TOML](https://toml.io) format and contains a TOML table of drivers. See the [driver list](../reference/driver_list.md) reference for more detail.

## Adding a Driver

While you can edit `dbc.toml` manually, dbc has subcommands for working with the list.
To add a driver to the list, use `dbc add`:

```console
$ dbc add mysql
added mysql to driver list
use `dbc sync` to install the drivers in the list
$ cat dbc.toml
[drivers]
[drivers.mysql]
```

When run, the `add` command automatically checks that a driver matching the pattern exists in the [driver registry](../concepts/driver_registry.md) and will fail if a matching driver can't be found.

!!! note

    `dbc add` accepts the same syntax for driver names and versions as `dbc install`. See the [Installing Drivers](installing.md).

If you look closely at the above output, you'll notice that it's telling you to run `dbc sync` to install the driver(s) in the list. This is because `dbc add` only modifies the driver list and you need to use `dbc sync` to actually install the driver you just added.

### Adding Multiple Drivers

{{ since_version('v0.2.0') }}

You can add multiple drivers in a single command:

```console
$ dbc add mysql snowflake
added mysql to driver list
added snowflake to driver list
use `dbc sync` to install the drivers in the list
$ cat dbc.toml
[drivers]
[drivers.mysql]
[drivers.snowflake]
```

Version constraints can be specified for each driver individually:

```console
$ dbc add "mysql=0.1.0" "snowflake>=1.0.0"
added mysql to driver list with constraint =0.1.0
added snowflake to driver list with constraint >=1.0.0
use `dbc sync` to install the drivers in the list
```

## Synchronizing

Use `dbc sync` to ensure that all the drivers in a driver list are installed:

```console
$ dbc sync
✓ mysql-0.1.0
Done!
```

The first time you run `dbc sync`, dbc creates a [lockfile](#lockfile) called `dbc.lock` in the same directory as the driver list.

When you run `dbc sync` and a lockfile already exists, dbc will install the exact versions in the lockfile.
To upgrade the versions in the lockfile, delete the lockfile and run `dbc sync`.

## Lockfile

`dbc sync` automatically creates a lockfile called `dbc.lock` in the same directory as the driver list.

The lockfile records the exact version of the drivers that were installed, including version, platform, and a checksum:

```console
$ cat dbc.lock
version = 1

[[drivers]]
name = 'mysql'
version = '0.1.0'
platform = 'macos_arm64'
checksum = 'e989f8c49262359093f03e2f43a796b163d2774de519e07cef14ebd63590c81d'
```

Every time you run `dbc sync`, this file is updated with the exact information about each driver that was installed.
It's a good idea to track `dbc.lock` as well as `dbc.toml` in version control if you want to ensure a completely reproducible set of drivers.

## Version Constraints

Each driver in a driver list can optionally include a version constraint which dbc will respect when you run `dbc sync`. You can add a driver to the list with the same syntax as you used for `dbc install`, see [Installing Drivers](installing.md).

```console
$ dbc add "mysql=0.1.0"
added mysql to driver list with constraint =0.1.0
use `dbc sync` to install the drivers in the list
$ cat dbc.toml
[drivers]
[drivers.mysql]
version = '=0.1.0'
```

## Pre-release Versions

{{ since_version('v0.2.0') }}

### Allowing Pre-release Versions

By default, when you add a driver to a driver list, dbc will only consider stable (non-pre-release) versions. If you want to allow pre-release versions when running `dbc sync`, use the `--pre` flag with `dbc add`:

```console
$ dbc add --pre mysql
added mysql to driver list
use `dbc sync` to install the drivers in the list
$ cat dbc.toml
[drivers]
[drivers.mysql]
prerelease = 'allow'
```

The `prerelease = 'allow'` field tells `dbc sync` to consider pre-release versions when resolving which version to install.

!!! note
    The `prerelease = 'allow'` field only affects implicit version resolution. When your version constraint unambiguously references a pre-release (by including a pre-release suffix like `-beta.1`), that constraint will match pre-release versions regardless of the `prerelease` field.

### Adding Specific Pre-release Versions

You can add a driver with a version constraint that references a specific pre-release version without using the `--pre` flag. When your version constraint unambiguously references a pre-release by including a pre-release suffix, the `prerelease` field is not added:

```console
$ dbc add "mysql=1.0.0-beta.1"
added mysql to driver list with constraint =1.0.0-beta.1
use `dbc sync` to install the drivers in the list
$ cat dbc.toml
[drivers]
[drivers.mysql]
version = '=1.0.0-beta.1'
```

The version constraint `=1.0.0-beta.1` unambiguously indicates you want a pre-release, so `prerelease = 'allow'` is not needed.

However, if your version constraint is ambiguous and only a pre-release version satisfies it, `dbc sync` will fail rather than install the pre-release. For example, if a driver has versions `0.1.0` and `0.1.1-beta.1`:

```toml
[drivers.mysql]
version = '>0.1.0'
# dbc sync will FAIL, not install 0.1.1-beta.1
```

To allow `0.1.1-beta.1` to be installed in this case, you must either:

- Use `dbc add --pre` to add `prerelease = 'allow'`
- Change the constraint to reference the pre-release: `version = '>=0.1.1-beta.1'`

## Removing Drivers

Drivers can be removed from a driver list with the `dbc remove` command:

```console
$ dbc remove mysql
removed 'mysql' from driver list
```

## Using `pyproject.toml`

If your project already has a `pyproject.toml` (common in Python projects), you can embed dbc's driver list directly inside it under a `[tool.dbc]` section, following the [PEP 518](https://peps.python.org/pep-0518/) convention for tool-specific metadata. This avoids adding a separate `dbc.toml` to your project root.

### Initializing

To add a `[tool.dbc]` section to your existing (or new) `pyproject.toml`:

```console
$ dbc init --pyproject
```

This creates or appends to `pyproject.toml`:

```toml
[tool.dbc.drivers]
```

If `pyproject.toml` doesn't exist yet, it will be created. If it already exists, the `[tool.dbc.drivers]` section is appended without modifying any existing content.

### Format

The structure under `[tool.dbc]` mirrors a standalone `dbc.toml` exactly. Everything that works in `dbc.toml` works under `[tool.dbc]`:

```toml
[project]
name = "my-python-project"
version = "1.0.0"

[[tool.dbc.registries]]
url = "https://custom-registry.example.com"
name = "Custom"

[tool.dbc.drivers]

[tool.dbc.drivers.duckdb]
version = '=1.4.0'

[tool.dbc.drivers.postgresql]
prerelease = 'allow'
```

### Format Preservation

When dbc modifies the `[tool.dbc]` section (via `dbc add` or `dbc remove`), it preserves all content outside that section — including comments, formatting, key ordering, and other `[tool.*]` sections. Only the `[tool.dbc]` section itself is rewritten.

### Workflow Example

```console
$ cat pyproject.toml
[project]
name = "my-project"

[tool.dbc.drivers]
[tool.dbc.drivers.duckdb]

$ dbc add postgresql
added postgresql to driver list
use `dbc sync` to install the drivers in the list

$ dbc sync
✓ duckdb-1.4.0
✓ postgresql-1.2.0
Done!
```

### Lockfile

The lockfile is always named `dbc.lock`, regardless of whether the driver list lives in `dbc.toml` or `pyproject.toml`. This ensures:

- The lockfile clearly belongs to dbc (not ambiguous with other tools)
- Migrating from `dbc.toml` to `pyproject.toml` (or vice versa) is seamless — no lockfile rename needed
- Multiple tools can each have their own lockfile without collision

### Explicit Path

You can also point commands directly at a `pyproject.toml`:

```console
$ dbc add --path pyproject.toml mysql
$ dbc sync --path pyproject.toml
```

### Migrating from `dbc.toml`

To migrate an existing `dbc.toml` to `pyproject.toml`:

1. Run `dbc init --pyproject` to create the `[tool.dbc]` section
2. Move the contents of your `dbc.toml` under `[tool.dbc]`, prefixing table headers with `tool.dbc.` (e.g. `[drivers.mysql]` becomes `[tool.dbc.drivers.mysql]`)
3. Delete `dbc.toml`
4. Your existing `dbc.lock` continues to work as-is

## Using a Custom Filename

By default, dbc assumes a driver list has the filename `dbc.toml`. However, you can override this if you prefer another name or want to maintain multiple driver lists in one project (e.g., separate development and production lists).

All of the commands shown earlier on this page allow you to override the filename, for example:

```console
$ dbc init drivers-dev.toml
$ dbc add --path drivers-dev.toml mysql
added mysql to driver list
use `dbc sync` to install the drivers in the list
$ dbc sync --path drivers-dev.toml
✓ mysql-0.1.0
Done!
$ dbc remove --path drivers-dev.toml mysql
removed 'mysql' from driver list
```
