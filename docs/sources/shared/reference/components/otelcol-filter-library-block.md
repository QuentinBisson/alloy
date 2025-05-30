---
description: Shared content, otelcol filter library block
headless: true
---

This block specifies properties to match the implementation library against:

* More than one `library` block can be defined.
* A match occurs if the span's implementation library matches at least one `library` block.

The following arguments are supported:

| Name      | Type     | Description                   | Default | Required |
| --------- | -------- | ----------------------------- | ------- | -------- |
| `name`    | `string` | The attribute key.            |         | yes      |
| `version` | `string` | The version to match against. | null    | no       |

If `version` is unset, any version matches.
If you set `version` to an empty string, it only matches a library version, which is also an empty string.
