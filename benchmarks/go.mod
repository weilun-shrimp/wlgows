module github.com/weilun-shrimp/wlgows/v7/benchmarks

go 1.24.0

require (
	github.com/coder/websocket v1.8.15
	github.com/gobwas/ws v1.4.0
	github.com/gorilla/websocket v1.5.3
	github.com/weilun-shrimp/wlgows/v7 v7.0.0
)

require (
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	golang.org/x/sys v0.6.0 // indirect
)

// The benchmarks run against this checkout, not a published release.
replace github.com/weilun-shrimp/wlgows/v7 => ../
