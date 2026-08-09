[English](./LISTENER_README.md) · **繁體中文**

# Listener

`Listener` 幫你跑 read loop。它從連線上讀取 frame、擋掉 RFC 6455 不允許的那些、
把分段的 message 組起來，然後依 opcode 呼叫你設定的 hook。

它懂協定的**形狀**，不管協定的**政策**。RFC 加在接收端身上的每一項義務，最後都落
在某個 hook 上，不會落在 Listener 裡面。

## Contents

- [Quick start](#quick-start) — 一條連線，從頭到尾
- [它不會替你做的三件事](#它不會替你做的三件事) — [關閉](#它不會關閉連線) · [寫出](#它不會寫出任何東西) · [存活偵測](#它沒有-ping-pong-存活偵測)
- [Configuration](#configuration) — `SetConfig`、`GetConfig`，以及哪一項一定要設對
- [Hooks](#hooks) — 哪種 frame 走到哪個 hook，以及 RFC 反過來要求你什麼
- [Errors](#errors) — 四類，以及哪幾類需要回一個 close frame
- [暫停與重新開始](#暫停與重新開始) — 結束一次執行，以及重新開始

library 的其他部分 —— 送出、streaming、存活偵測、handshake、lock —— 在
[main README](./README.zh-TW.md)。

## Quick start

`conn` 是一條已經 handshake 完成的 `*wlgows.ServerConn`。這段從頭到尾處理一條連
線，也涵蓋了 `Listen` 所有可能的回傳方式。`Pong` 是唯一刻意留 nil 的 hook ——
5.5.3 說收到 pong 時 MUST NOT 回應，而 nil hook 做的正好就是這件事。

`SetConfig` 是一次把整份設定交出去，隨時都能呼叫 —— `Listen` 之前、兩次執行之
間，或在某個 hook 裡面。

```go
func handleConn(conn *wlgows.ServerConn) {
	defer conn.Close() // 你的責任：Listener 什麼都不會關

	listener := wlgows.NewListener(conn)
	listener.SetConfig(wlgows.ListenerConfig{
		PeerIsClient:         true,             // 我們是 server，所以對端會 mask
		MaxMsgPayloadByteLen: 10 * 1024 * 1024, // 每個 message 10 MB
		MaxMsgFrameCount:     4000,             // 見 Configuration
		FrameReadTimeout:     60 * time.Second,

		Text: func(frames wlgows.Frames) {
			log.Printf("text: %s", frames.String())
		},
		Binary: func(frames wlgows.Frames) {
			log.Printf("binary: %d bytes", frames.ByteLen())
		},
		Ping: func(f *wlgows.Frame) {
			// 5.5.2：回一個 pong，把 f.PayloadData 原封不動帶回去。送出是你的事。
		},
		Close: func(f *wlgows.Frame) {
			payload, _ := f.GetClosePayload() // Listener 已經驗過了
			// 5.5.1：回一個 close，內容你決定，然後停止讀取。
			listener.PauseListen(nil) // nil：對端已經說了原因，Listen 回 nil
		},
		Unknown: func(f *wlgows.Frame) {
			// 5.2 保留的 opcode：回 close 1002，然後停止。
			listener.PauseListen(errors.New("reserved opcode")) // Listen 會回傳這個
		},
	})

	// 會 block，直到讀取失敗、某個 frame 違規，或 PauseListen 被呼叫。
	switch err := listener.Listen(); {
	// PauseListen(nil) —— 這裡只有 Close hook 會這樣做，所以是對端關閉，沒有別的
	// 好說。
	case err == nil:

	// 一個 byte 都還沒讀就回來了，所以跟這條連線無關。
	case errors.Is(err, wlgows.ErrListenerConnIsNil),
		errors.Is(err, wlgows.ErrListenerIsListening):
		log.Println("listener misuse:", err)

	// 某個 frame 違規、socket 死了，或是無從歸責的東西。只有第一種會拿到
	// payload —— 一條死掉的 socket 沒什麼好告訴對方的。
	default:
		if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
			// 用它回一個 close，然後關閉。
		}
		log.Println("closing:", err)
	}
}
```

Listener 什麼都不寫、什麼都不關，所以上面每一個「回應」都是註解而不是呼叫：送上
線的東西是你的，hook 只負責告訴你「什麼時候」。`PauseListen` 是它在那裡唯一幫你
做的事 —— 它結束這次執行，讓這個 function 可以返回，並帶著你交給它的原因。

## 它不會替你做的三件事

### 它不會關閉連線

收到 close frame 不會、讀取錯誤不會、payload 超過上限不會、遇到未知 opcode 也不
會。它只回報然後返回。`Listen` 回傳一個 error，是**你**該關閉的理由，絕不是它已經
關掉的意思。

這不是潔癖。RFC 6455 7.1.1 給兩側的義務本來就不一樣 —— close frame 交換完之後，
server MUST 立刻關掉 TCP 連線，而 client SHOULD 等 server 先關，等一段合理時間後
才放棄。Listener 不知道自己在哪一側。

### 它不會寫出任何東西

不回 pong、不回 close、不回音。它只讀。上面 quick start 裡每一個 `Send*` 都是你
的，寫在你的 hook 裡。

所以一個**完全沒設 hook 的 Listener，是一個什麼都不回應但完全合規的讀取端** ——
它會很安分地待在那裡，而對端則一直等著永遠不會來的 pong。

### 它沒有 ping pong 存活偵測

`Ping` 和 `Pong` hook 只告訴你「有一個 frame 到了」。要偵測對端死掉，是你自己的計
時器：

- 從你自己的 goroutine 定期送出 ping
- 收到**任何** pong 就把 deadline 重設 —— 不要去比對 payload
- deadline 過了還沒有 pong，對端就是不在了；關閉

不要比對 payload，是因為有兩種合規行為會讓比對失效：RFC 6455 5.5.3 允許沒人問就
送的 pong，而 5.5.2 允許在好幾個 ping 還沒回的情況下只回最新的那一個。

`FrameReadTimeout` 蓋不到這件事。它是在**完全沒有 byte 進來**時才觸發 —— 一個持
續送資料、卻不理你 ping 的對端，在它眼裡活得好好的。

## Configuration

`SetConfig` 是把整個 `ListenerConfig` 用複製的方式、在 lock 底下交出去，所以從任
何 goroutine 呼叫都安全，包括在 read loop 裡的 hook 中呼叫。連線不在裡面 —— 那是
`NewListener` 的，而且在 Listener 的一生中不會變。

每一項，以及沒填它代表什麼：

| 項目 | 型別 | 零值 | |
|---|---|---|---|
| `PeerIsClient` | `bool` | 對端是 **server** | 對端在哪一側，這決定 masking（5.1） |
| `MaxMsgPayloadByteLen` | `uint64` | 不限制 | 單一 message 的 payload 額度，每個 header 都會檢查 |
| `MaxMsgFrameCount` | `uint64` | 不限制 | 單一 message 最多能由幾個 frame 組成 |
| `FrameReadTimeout` | `time.Duration` | 不逾時 | 每個 frame 一份，在每次讀取前設定 |
| `Ping` | `func(*Frame)` | 丟掉那些 frame | [5.5.2](#hooks)：回一個 pong，原封不動帶回 payload |
| `Pong` | `func(*Frame)` | 丟掉那些 frame | [5.5.3](#hooks)：MUST NOT 回應 —— nil 正好就是合規的設定 |
| `Close` | `func(*Frame)` | 丟掉那些 frame | [5.5.1](#hooks)：回一個 close，然後 `PauseListen` |
| `Text` | `func(Frames)` | 丟掉那些 message | 一整個 message，已經驗過是 UTF-8 |
| `Binary` | `func(Frames)` | 丟掉那些 message | 一整個 message，任意 byte |
| `Data` | `func(*Frame)` | 改由 `Text`/`Binary` 組裝 | [每個 data frame 原樣交出](#hooks)，什麼都不留 |
| `Unknown` | `func(*Frame)` | 丟掉那些 frame | [5.2](#hooks) 保留的 opcode：回 1002，然後停止 |

**每次都是整份。** 你沒填的欄位會被設成零值，而不是「維持原樣」，所以只改一項的
做法是先把其餘的讀回來：

```go
config := listener.GetConfig() // 目前正在跑的那份的複本
config.MaxMsgFrameCount = 4000
listener.SetConfig(config)
```

這個來回也是 hook 在執行中改設定的方式。已經在處理中的那個 frame 會用它一開始拿
到的值走完，所以改動會落在下一個 frame，而不是卡在這個 frame 的中間。

沒有任何一項是必填。每一項的零值都能用，所以一個完全沒呼叫過 `SetConfig` 的
Listener 仍然讀得動 —— 不過 `PeerIsClient` 是一定要設對的那一項，因為它的零值代表
「對端是 server」，而一個沒設它的 server 會拒絕 client 送來的每一個 frame。

`MaxMsgPayloadByteLen` 則是最值得設的那一項。對端可以用 10 byte 的 header 宣告
10 GB 的 payload，沒有上限的話，那個宣告會在第一個 payload byte 抵達之前，就變成
10 GB 的配置。

**`MaxMsgFrameCount` 怎麼挑。** 對端怎麼分段不是你能決定的，所以從「你願意接受的
最小 fragment」開始算：

	MaxMsgFrameCount = MaxMsgPayloadByteLen / 你預期的最小 fragment

10 MB 的額度、以 4 KB 分段就是 2560 個 frame，所以設 4000 還有餘裕。寧可設高：設
太高只是把 `MaxMsgPayloadByteLen` 已經擋住的那道記憶體上限放鬆一點，而設太低會擋
掉一個合規對端本來就有權送出的 message，而且你不會知道為什麼。它之所以存在，是因
為空的 continuation frame 會被丟掉而不是留著，所以它們永遠不會消耗 byte 額度 ——
否則對端可以用一堆不花成本的 frame，把一個 message 永遠掛在那裡。

**設了 `Data` 的話，byte 額度要照整串的量抓**，不是照單一 frame：它仍然是整個
message 一起扣的，而 frame 之間什麼都不留。那代價是什麼，見 [Hooks](#hooks) 裡
`Data` 的那一段。

每一項都有自己的完整細節 —— 涵蓋哪些 frame，以及那些數字背後的理由：

```bash
go doc github.com/weilun-shrimp/wlgows/v3.ListenerConfig
```

## Hooks

由 read loop 一次呼叫一個。一個 hook 執行多久，就**擋住 loop 多久** —— 這段期間
不會讀進任何 frame，包括還等著被回應的 ping —— 所以慢的 hook 應該把工作丟給自己的
goroutine 或 queue。它們不會被同時呼叫，這正好保住了 TCP 和 RFC 6455 5.4 白送給你
的 message 順序。

nil 的 hook 會把那些 frame 丟掉。

| hook | 收到什麼 | RFC 反過來要求你什麼 |
|---|---|---|
| `Ping` | 一個 frame | 5.5.2：MUST 回一個 pong 並原封不動帶回 payload，除非 close 已經來過 |
| `Pong` | 一個 frame | 5.5.3：MUST NOT 回應 |
| `Close` | 一個 frame | 5.5.1：MUST 回一個 close，然後關閉。`Frame.GetClosePayload` 會把 status code 和 reason 解出來，兩者都已經驗過 |
| `Text` | 一整個 message | 什麼都不用 —— payload 已經驗過是合法 UTF-8（5.6、8.1） |
| `Binary` | 一整個 message | 什麼都不用 —— 就是任意 byte |
| `Data` | 一個 data frame | **請小心使用。** FIN 要自己顧，text message 的 5.6 UTF-8 也要自己驗：不合法就回 1007。只給大到放不下的 message —— 其他情況請用 `Text` 或 `Binary` |
| `Unknown` | 一個 frame | 5.2 保留的 opcode。這是 protocol error：回 1002 然後關閉 |

`Text` 和 `Binary` 收到的是完整的 message，已經跨所有 fragment 組好。你不會看到分
段，而夾在兩個 fragment 中間抵達的 control frame 會走它自己的 hook，不會干擾正在
組裝的 message。

text message 在送到 `Text` 之前會依 RFC 6455 5.6 驗證，而且驗的是**接起來之後**的
byte：一個 frame 可能剛好切在某個 rune 的中間，所以逐 frame 驗會誤殺合規的
message。`Binary` 則完全不驗 —— 5.6 本來就把 binary payload 定成任意 byte。

`Data` 會取代整個組裝流程：每個 data frame 直接送到它手上，一個都不留，所以 `Text`
和 `Binary` 完全不會被呼叫。它是這份設定裡最鋒利的那一項 —— 只有在你真的要 frame
本身時才用它，例如大到放不下的傳輸、或你要轉手送出去的 stream，而且要清楚知道隨之
而來的是什麼。放得進記憶體的 message 屬於 `Text` 或 `Binary`，那兩個會幫你把該做
的都做完。

隨之而來的是：FIN 要自己看，而 5.6 沒辦法只憑一個 frame 判斷 —— 它可能切在 rune
中間 —— 所以沒有任何東西會幫你驗。`Listener.GetCurrentMsgOpcode` 會告訴你這個
message 是不是 text、也就是欠不欠你那道驗證，因為 continuation frame 本身不帶
type。各種額度、5.4，以及空 continuation frame 會被丟掉這件事都沒有變，所以送到這
裡的，就是這個 message 真正由哪些 frame 組成。請在兩個 message 之間設定它，不要在
一個 message 進行中設。

把一個 message 直接收進磁碟，不管它多大 —— 因為 frame 之間什麼都不留，記憶體維持
平坦：

```go
out, err := os.Create("./received.bin")
if err != nil {
	return err
}
defer out.Close()

listener := wlgows.NewListener(conn)
listener.SetConfig(wlgows.ListenerConfig{
	PeerIsClient:         true,
	MaxMsgPayloadByteLen: 2 * 1024 * 1024 * 1024, // 整個 message 共用，所以要照整串的量抓
	FrameReadTimeout:     60 * time.Second,

	Data: func(f *wlgows.Frame) {
		// 只收 binary。text message 會需要在接起來的 byte 上驗 5.6，這也是為什麼
		// 值得問一下 opcode。
		if listener.GetCurrentMsgOpcode() != wlgows.OpcodeBinary {
			// 拒絕它 —— close 1003，或你的協定說了算。
			listener.PauseListen(errors.New("peer streamed text"))
			return
		}
		if _, err := out.Write(f.PayloadData); err != nil {
			listener.PauseListen(err) // 是你的磁碟，不是對端的錯
			return
		}
		if f.FIN { // 沒有別的東西標示結束
			log.Println("message complete")
		}
	},
	Ping: func(f *wlgows.Frame) {
		// 5.5.2：回一個 pong，原封不動帶回 f.PayloadData。
	},
	Close: func(f *wlgows.Frame) {
		// 5.5.1：回一個 close，然後停止。
		listener.PauseListen(nil)
	},
})

// 不管是什麼結束了它：讀取錯誤、違規的 frame，或上面某個 pause。
return listener.Listen()
```

這裡最需要想一下的是 `MaxMsgPayloadByteLen`。它仍然是整個 message 一起扣的，所以
必須涵蓋整串資料 —— 而因為它同時也是在 header 就限制單一 frame 的那個數字，開這麼
大就等於允許某一個 frame 把它整碗端走。「每個 frame 都抓很緊、但 message 可以很
長」是這裡唯一表達不出來的東西 —— `Conn.GetNextFrame(max)` 可以，因為那個上限是每
次讀取一份，不是每個 message 一份。

[`stream_server`](./example/stream_server/main.go) 就是這個 hook 的完整示範：一個
一個 frame 收下檔案、順路算 hash，額度也照整串的量設好。

**收到 close frame 之後就不要再讀了。** RFC 6455 5.5.1 說一個 endpoint 在 Close 抵
達之後 MUST NOT 再處理任何 data frame。Listener 不會替你強制這件事 —— 它把 frame
交給 `Close` 然後繼續讀 —— 所以你的 `Close` hook 一定要呼叫 `PauseListen`，否則
close 之後才到的 message 仍然會走進 `Text` 或 `Binary`。

## Errors

`Listen` 回 `nil` 的情況是：`PauseListen(nil)` 結束了一次執行，而且沒有別的事出
錯。改成帶一個 error 進去，回來的就是它，所以由你自己的程式結束的執行，會說得出原
因 —— 一個關機訊號、一個你自己的計時器守著的 deadline、一條這個 package 不認識的
規則。誰先 pause 誰算數；第二個 pause 會被丟掉，而不是覆蓋掉這次執行真正結束的原
因。

pause 和一個失敗的讀取常常是同一件事，因為把連線關掉正是喚醒卡住的讀取的方式。這
兩者會**合併**回傳，所以 `errors.Is` 找得到你傳進去的原因，也找得到 socket 自己的
error。nil 的 pause 則只留下讀取的 error —— 這也是為什麼「回傳不是 nil」並不一定
代表是對端或協定的錯。

那些是你的東西，`StandardClosePayloadFor` 認不得：它只替協定本身的 error 作答，其
餘一律回 nil，所以你自己發明的 error 會落在下面的第 4 類，除非你先自己對應。

其餘每一種回傳，帶的都是這個 package 自己拋出的 error。`io.EOF` 不是它看起來的那
種客氣道別：它表示對端**沒有**送 close frame 就把 TCP 連線斷掉了，也就是 §7.4.1 說
的 1006。

這些 error 分成四類，而且處理方式不一樣。分類這件事，就是全部的工作。

### 1. 某個 frame 違規了

對端違反了 RFC 6455。回一個 close frame，然後關閉。`StandardClosePayloadFor` 就是
7.4.1 那張表：

| error | 用什麼回應 |
|---|---|
| `ErrReservedBitsSet` | 1002 |
| `ErrFrameNotMasked`、`ErrFrameMasked` | 1002 |
| `ErrControlFrameFragmented` | 1002 |
| `ErrControlFramePayloadTooLong` | 1002 |
| `ErrClosePayloadTooShort` | 1002 |
| `ErrContinuationFrameWithoutMsg`、`ErrDataFrameDuringMsg` | 1002 |
| `ErrInvalidCloseStatusCode` | 1002 |
| `ErrInvalidUTF8` | 1007 |
| `ErrFrameByteLengthExceeded` | 1009 |
| `ErrMsgFrameCountExceeded` | 1009 |

**只要發生任何一個，這條 stream 就不能再用了。** 被拒絕的那個 frame 已經被讀掉一
部分，所以下一次讀取會從 frame 中間開始，把 payload 的 byte 當成 header 來解析。永
遠不要接著讀 —— 寫的方向是獨立的，所以 close frame 還是送得出去，但你不能再讀。

### 2. 連線本身斷了

`io.EOF`、`os.ErrDeadlineExceeded`、`net.ErrClosed`、connection reset。這些來自
socket，不是來自這個 package，而且對端沒有違反任何規則。沒有對象可以送 close frame
了。§7.4.1 把這種情況稱為 **1006 abnormal closure**，並禁止把 1006 送上線，因為那
是一個 endpoint 對自己的紀錄 —— 所以記下來然後關閉。

讀取逾時是你自己的 `FrameReadTimeout` 政策，不是對端的錯。socket 通常還寫得出去，
所以你*可以*先送一個 1000 或 1001。只有你能決定要哪一個，所以
`StandardClosePayloadFor` 不會替你決定。

### 3. 你把 Listener 用錯了

`ErrListenerConnIsNil` 表示這個 Listener 是手工建出來的、不是用 `NewListener`，所
以它沒有連線可讀；`ErrListenerIsListening` 表示第二個 `Listen` 和第一個重疊了。兩
者都在讀進任何一個 byte 之前就回來了。

**遇到這兩個不要關連線。** `ErrListenerIsListening` 表示另一個 goroutine 正握著一
個跑著的 `Listen` —— 關掉會終結一個健康的 session。這些是你程式碼裡的 bug，不是要
處理的狀況。

### 4. 完全是別的東西

一個從自訂 `net.Conn`、TLS 層或 mock 冒出來、經由 `GetNextFrame` 浮上來的普通
`errors.New("...")`。這個 package 說不出這是誰的錯。

當成第 2 類處理 —— 關閉，什麼都不送。§7.1.1 允許不送 close frame 就關閉，所以沉默
永遠合法；而猜一個 1002 是在指控一個可能什麼都沒做錯的對端，而且對端拿到錯的 status
code 也救不回來。如果你知道自己的 error 是什麼意思，請在呼叫
`StandardClosePayloadFor` **之前**自己對應，因為它只會回 `nil`。

### 兜起來

`StandardClosePayloadFor` 只對第 1 類回傳 payload，其餘一律 `nil`。`nil` 的意思是
*無從歸責*，不是*連線很健康*，也不是*關掉連線* —— 第 2、4 類要關，第 3 類則絕對不
能關：

```go
err := listener.Listen()
switch {
case err == nil: // PauseListen(nil)，沒有什麼要回應的

case errors.Is(err, wlgows.ErrListenerConnIsNil),
	errors.Is(err, wlgows.ErrListenerIsListening):
	log.Printf("listener misuse: %v", err) // 第 3 類：別動這條連線

default:
	if payload := wlgows.StandardClosePayloadFor(err); payload != nil {
		// 第 1 類：關閉之前先用它回應
	}
	// 第 1、2 類都要關掉連線，而 7.1.1 說了哪一側先關。兩個呼叫都是你的。
}
```

`Reason` 一律留空。要不要告訴對端出了什麼事，是這個 package 沒有意見的政策 —— 想
要的話，送出前自己填。

保留的 opcode 不會走到這條路上。它不是 error：那個 frame 會原封不動送進 `Unknown`
hook，而回 1002 是你要做的事。

## 暫停與重新開始

`PauseListen(err)` 結束這次執行，而 `Listen` 會回傳 `err`。傳 nil 表示停下來、但沒
有什麼要回報的。它從別的 goroutine 呼叫是安全的，在沒有任何執行時呼叫也是安全的
—— 那種情況下它什麼都不做，連那個 error 也一起丟掉。

pause 只會在**兩個 frame 之間**被注意到，因為 loop 大部分時間都停在 `GetNextFrame`
裡面。對端安靜不動時，`PauseListen` 會立刻返回，而讀取仍然卡著 —— 只有關掉連線能
把它解開，而那是你要做的事。

重新開始會從上次停下的地方接續。暫停時只組了一半的 message 仍然是開著的，所以中間
可以更換設定而不會掉 frame。
