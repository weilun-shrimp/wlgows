**English** · [繁體中文](./README.zh-TW.md)

# Autobahn: server

This folder tests the wlgows server with the
[Autobahn|Testsuite](https://github.com/crossbario/autobahn-testsuite). See
[AUTOBAHN.md](../AUTOBAHN.md) for what Autobahn is, its config fields and how
to read the report.

Everything runs on your machine. Autobahn runs in Docker and acts as the
client, in its `fuzzingclient` mode. It connects to `main.go`, an echo server
on port 9001.

## Run

Docker must reach your machine at `host.docker.internal`. Docker Desktop
defines that name for you. Docker Engine on Linux does not, so add
`--add-host=host.docker.internal:host-gateway` to `docker run`.

Run both commands from this folder.

1. Start the server:

   ```sh
   go run .
   ```

2. In another terminal, start Autobahn:

   ```sh
   docker run -it --rm \
     -v "$PWD/config_fuzzing.json:/config/config_fuzzing.json" \
     -v "$PWD/reports:/reports" \
     crossbario/autobahn-testsuite \
     wstest -m fuzzingclient -s /config/config_fuzzing.json
   ```

   The first run downloads the image. A full run takes a few minutes.

3. Open the report. See [The report](#the-report).

## The config

`config_fuzzing.json` tells Autobahn what to test. `servers` lists the servers
to test. Each has an `agent`, the name in the report, and a `url`. In the `url`,
`host.docker.internal` is your machine, as seen from inside Docker. The other
fields are in [AUTOBAHN.md](../AUTOBAHN.md#the-config).

## The report

The report is written to `reports/` in this folder. Its main page is
`reports/index.html`. Open it in a browser, or from this folder:

```sh
open reports/index.html       # macOS
xdg-open reports/index.html   # Linux
start reports\index.html      # Windows
```

How to read it is in [AUTOBAHN.md](../AUTOBAHN.md#reading-the-report).
