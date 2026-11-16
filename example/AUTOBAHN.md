**English** · [繁體中文](./AUTOBAHN.zh-TW.md)

# Autobahn

The [Autobahn|Testsuite](https://github.com/crossbario/autobahn-testsuite) is
the standard WebSocket conformance suite. It runs about 500 cases covering
RFC 6455 and RFC 7692. They check framing, fragmentation, ping and pong, close,
UTF-8 and permessage-deflate.

Its tool, `wstest`, plays either side of the connection. The mode is named
after what `wstest` acts as, not after what is tested:

| Folder | Tests | `wstest` mode | `wstest` acts as |
|---|---|---|---|
| [autobahn_server](./autobahn_server/README.md) | the wlgows server | `fuzzingclient` | the client |
| [autobahn_client](./autobahn_client/README.md) | the wlgows client | `fuzzingserver` | the server |

This page covers what both sides share: the config fields and how to read the
report. Each folder's README covers how to run it and where its report is.

## The config

Each folder has a `config_fuzzing.json`. These fields work the same in both
modes:

| Field | Meaning |
|---|---|
| `outdir` | Where the report goes, inside Docker. The `docker run` command maps it to the folder's `reports/`. |
| `cases` | The cases to run. `"*"` runs all of them. `"6.*"` runs section 6 only. |
| `exclude-cases` | Cases to skip, for example `"9.*"` to skip the large messages. |
| `exclude-agent-cases` | The same, but only for one `agent`, the name of the tested side in the report. |

## Reading the report

Open the folder's `reports/index.html` in a browser, such as
`example/autobahn_server/reports/index.html` or
`example/autobahn_client/reports/index.html`. The top of the page explains each
result.

In each case's row:

- Click the result, such as "Pass", to open the case's own page: what was sent,
  what came back, and what was expected.
- The case number only jumps to its description on the same page.
- The small cell right of the result is the close code. "Toggle Details", in
  the bottom-right corner of the window, hides it.

The page doesn't explain Unimplemented: an optional feature, such as a
compression setting, was not agreed. It has no link, so open its page directly.
It sits next to `index.html`, such as `wlgows_case_13_3_1.html` for case
13.3.1.
