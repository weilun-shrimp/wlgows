[English](./README.md) · **繁體中文**

# Autobahn：client

這個資料夾用 [Autobahn|Testsuite](https://github.com/crossbario/autobahn-testsuite)
測試 wlgows 的 client。Autobahn 是什麼、它的設定欄位，以及怎麼看報告，
請看 [AUTOBAHN.zh-TW.md](../AUTOBAHN.zh-TW.md)。

全部都在你的電腦上執行。Autobahn 跑在 Docker 裡，以 `fuzzingserver` 模式扮演
server，listen 在 port 9001。`main.go` 是一個 echo client，每個案例連到 Autobahn 一次。

## 執行

兩個指令都在這個資料夾裡執行。

1. 啟動 Autobahn：

   ```sh
   docker run -it --rm \
     -v "$PWD/config_fuzzing.json:/config/config_fuzzing.json" \
     -v "$PWD/reports:/reports" \
     -p 9001:9001 \
     crossbario/autobahn-testsuite \
     wstest -m fuzzingserver -s /config/config_fuzzing.json
   ```

   第一次執行會先下載 image。等它印出
   `Ok, will run 517 test cases for any clients connecting`。

2. 開另一個終端機，啟動 client：

   ```sh
   go run .
   ```

   完整跑一輪要幾分鐘。報告寫好後，client 就會結束。
   有些案例會印出 error。這是正常的：報告會顯示每個案例的結果。

3. 打開報告，做法請看[報告](#報告)。然後用 Ctrl+C 停掉 Autobahn。

## 設定檔

`config_fuzzing.json` 告訴 Autobahn 要測什麼。`url` 是 Autobahn 在 Docker
內部 listen 的位址。`docker run` 的 `-p 9001:9001` 讓你的電腦能用
`ws://127.0.0.1:9001` 連到它。其他欄位請看
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

在 13.3.x、13.5.x 和 13.7.x 案例中，Autobahn 的 server 回應
`client_max_window_bits=9`。在這個 window 下，wlgows 只允許 `flate.HuffmanOnly`
和 `flate.NoCompression`（函式庫的選擇），所以 client 在這些案例用
`flate.HuffmanOnly` echo，其他案例用 `flate.BestSpeed`。請看
[SENDING.zh-TW.md](../../SENDING.zh-TW.md#15-bit-以下的-window)。
