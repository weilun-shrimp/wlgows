[English](./README.md) · **繁體中文**

# Autobahn：server

這個資料夾用 [Autobahn|Testsuite](https://github.com/crossbario/autobahn-testsuite)
測試 wlgows 的 server。Autobahn 是什麼、它的設定欄位，以及怎麼看報告，
請看 [AUTOBAHN.zh-TW.md](../AUTOBAHN.zh-TW.md)。

全部都在你的電腦上執行。Autobahn 跑在 Docker 裡，以 `fuzzingclient` 模式扮演
client，連到 `main.go`。`main.go` 是一個 echo server，listen 在 port 9001。

## 執行

Docker 必須能透過 `host.docker.internal` 連到你的電腦。
Docker Desktop 已經內建這個名稱。Linux 上的 Docker Engine 沒有，
請在 `docker run` 加上 `--add-host=host.docker.internal:host-gateway`。

兩個指令都在這個資料夾裡執行。

1. 啟動 server：

   ```sh
   go run .
   ```

2. 開另一個終端機，啟動 Autobahn：

   ```sh
   docker run -it --rm \
     -v "$PWD/config_fuzzing.json:/config/config_fuzzing.json" \
     -v "$PWD/reports:/reports" \
     crossbario/autobahn-testsuite \
     wstest -m fuzzingclient -s /config/config_fuzzing.json
   ```

   第一次執行會先下載 image。完整跑一輪要幾分鐘。

3. 打開報告，做法請看[報告](#報告)。

## 設定檔

`config_fuzzing.json` 告訴 Autobahn 要測什麼。`servers` 列出要測的 server。
每一個都有 `agent` 和 `url`：`agent` 是報告裡顯示的名稱。
`url` 裡的 `host.docker.internal`，從 Docker 內部看就是你的電腦。其他欄位請看
[AUTOBAHN.zh-TW.md](../AUTOBAHN.zh-TW.md#設定檔)。

## 報告

報告會寫到這個資料夾裡的 `reports/`，主頁是 `reports/index.html`。
用瀏覽器打開它，或在這個資料夾裡執行：

```sh
open reports/index.html       # macOS
xdg-open reports/index.html   # Linux
start reports\index.html      # Windows
```

怎麼看報告，請看 [AUTOBAHN.zh-TW.md](../AUTOBAHN.zh-TW.md#怎麼看報告)。
