**English** · [繁體中文](./README.zh-TW.md)

# Autobahn: client

This folder tests the wlgows client with the
[Autobahn|Testsuite](https://github.com/crossbario/autobahn-testsuite). See
[AUTOBAHN.md](../AUTOBAHN.md) for what Autobahn is, its config fields and how
to read the report.

Everything runs on your machine. Autobahn runs in Docker and acts as the
server, in its `fuzzingserver` mode, on port 9001. `main.go`, an echo client,
connects to it once for each case, or twice for some 13.x cases (see
[The report](#the-report)).

## Run

Run both commands from this folder.

1. Start Autobahn:

   ```sh
   docker run -it --rm \
     -v "$PWD/config_fuzzing.json:/config/config_fuzzing.json" \
     -v "$PWD/reports:/reports" \
     -p 9001:9001 \
     crossbario/autobahn-testsuite \
     wstest -m fuzzingserver -s /config/config_fuzzing.json
   ```

   The first run downloads the image. Wait until it prints
   `Ok, will run 517 test cases for any clients connecting`.

2. In another terminal, start the client:

   ```sh
   go run .
   ```

   A full run takes a few minutes. The client exits when the report is
   written. Some cases print an error. This is expected: the report shows each
   case's result.

3. Open the report. See [The report](#the-report). Then stop Autobahn with
   Ctrl+C.

## The config

`config_fuzzing.json` tells Autobahn what to test. `url` is where Autobahn
listens, inside Docker. `-p 9001:9001` in `docker run` makes that port
`ws://127.0.0.1:9001` on your machine. The other fields are in
[AUTOBAHN.md](../AUTOBAHN.md#the-config).

## The report

The report is written to `reports/` in this folder. Its main page is
`reports/index.html`. Open it in a browser, or from this folder:

```sh
open reports/index.html       # macOS
xdg-open reports/index.html   # Linux
start reports\index.html      # Windows
```

How to read it is in [AUTOBAHN.md](../AUTOBAHN.md#reading-the-report).

In the cases 13.3.x, 13.5.x and 13.7.x, Autobahn's server responds with
`client_max_window_bits=9`. wlgows cannot send with a window under 15 bits, so
the handshake fails with `ErrHandshakeResponseDeflateUnsupported` (library
choice). The client then runs the case again with an offer that has no
`client_max_window_bits`. Autobahn keeps the last result, so 13.3.x and 13.5.x
show Unimplemented. In 13.7.x, the server then responds with
`client_no_context_takeover` only, which wlgows can use. See
[CLIENT.md](../../CLIENT.md).
