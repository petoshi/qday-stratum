module github.com/petoshi/qday-stratum

go 1.26.0

require (
	go.sia.tech/core v0.0.0-00010101000000-000000000000
	golang.org/x/crypto v0.54.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/cloudflare/circl v1.6.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
	lukechampine.com/frand v1.5.1 // indirect
)

replace go.sia.tech/core => ./qday/core
