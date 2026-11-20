[English](./AUTOBAHN.md) · **繁體中文**

# Autobahn

[Autobahn|Testsuite](https://github.com/crossbario/autobahn-testsuite) 是標準的
WebSocket 相容性測試。它有大約 500 個案例，涵蓋 RFC 6455 與 RFC 7692。
這些案例檢查 framing、fragmentation、ping 與 pong、close、UTF-8，以及
permessage-deflate。

它的工具 `wstest` 可以扮演連線的任一方。模式是依 `wstest` 扮演的角色命名，
不是依被測的那一方：

| 資料夾 | 測試對象 | `wstest` 模式 | `wstest` 扮演 |
|---|---|---|---|
| [autobahn_server](./autobahn_server/README.zh-TW.md) | wlgows 的 server | `fuzzingclient` | client |
| [autobahn_client](./autobahn_client/README.zh-TW.md) | wlgows 的 client | `fuzzingserver` | server |

這一頁說明兩邊共用的部分：設定欄位，以及怎麼看報告。
每個資料夾的 README 說明怎麼執行，以及報告在哪裡。

## 我們的結果

| 端 | OK | Non-Strict | Informational | Unimplemented | Failed |
|---|---|---|---|---|---|
| Server | 510 | 4 | 3 | 0 | 0 |
| Client | 510 | 4 | 3 | 0 | 0 |

wlgows v7，`wstest` 25.10.1，每一端各跑全部 517 個案例。

## 設定檔

每個資料夾都有一個 `config_fuzzing.json`。以下欄位在兩種模式下的用法相同：

| 欄位 | 意思 |
|---|---|
| `outdir` | 報告在 Docker 裡的輸出位置。`docker run` 指令會把它對應到資料夾裡的 `reports/`。 |
| `cases` | 要跑的案例。`"*"` 跑全部，`"6.*"` 只跑第 6 節。 |
| `exclude-cases` | 要跳過的案例，例如 `"9.*"` 會跳過大型 message。 |
| `exclude-agent-cases` | 同上，但只套用在某一個 `agent`，也就是報告裡被測那一方的名稱。 |

## 怎麼看報告

用瀏覽器打開資料夾裡的 `reports/index.html`，例如
`example/autobahn_server/reports/index.html` 或
`example/autobahn_client/reports/index.html`。頁面最上方會說明每種結果。

在每個案例那一列：

- 點結果，例如「Pass」，會打開這個案例自己的頁面：送了什麼、回了什麼，
  以及預期是什麼。
- 點案例編號只會跳到同一頁裡的案例說明。
- 結果右邊的小格子是 close code。視窗右下角的「Toggle Details」可以把它隱藏。

報告頁面沒有說明 Unimplemented。它表示某個選用功能沒有協商出來，例如某種壓縮設定。
Unimplemented 沒有連結，所以要直接打開它的頁面。這個頁面和 `index.html` 放在一起，
檔名由 agent 和案例組成，例如案例 1.1.1 是 `wlgows_case_1_1_1.html`。
