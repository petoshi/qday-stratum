# QDAY STRATUM

## MAKE THE GPU EAT THE MEMPOOL.

QDAY already speaks BLAKE2b-256. Sia miners already know how to grind an
80-byte BLAKE2b header. They were separated by one missing piece: a local
translator that gives the miner a real QDAY block and puts the solved block
back on QDAY.

This is that piece.

```text
QDAY Wallet / Node
        ↓ authenticated getblocktemplate
qday-stratum
        ↓ Sia Stratum: 80-byte BLAKE2b job + target
Sia GPU or ASIC miner
        ↓ winning nonce
qday-stratum
        ↓ complete block
QDAY Wallet / Node
```

It is solo mining. There are no accounts, balances, payout thresholds or coins
held by somebody else's server. The QDAY node builds the candidate, includes
its mempool, adds every fee to the miner payout and pays the unlocked wallet
connected to the bridge.

One executable. One local port. No pool operator to annoy.

## What you need

- [QDAY Wallet or Node v0.8.0 or newer](https://github.com/petoshi/qday/releases/latest).
- An unlocked and synchronized QDAY wallet. Its built-in CPU miner may remain
  stopped.
- A miner that speaks the
  [SiaMining Stratum protocol](https://github.com/SiaMining/Stratum/blob/master/Stratum.md).
  The reference setup below uses
  [gominer](https://github.com/robvanmieghem/gominer).

The bridge never asks for the wallet password or seed phrase. It reads the
local `api.token`, requests block candidates and submits solved blocks.

## Start in three commands

Keep QDAY Wallet open, unlocked and synced. Stop its CPU miner if the GPU or
ASIC should do the work alone.

Linux:

```sh
./qday-stratum \
  -token-file "$HOME/.config/qday/qday-mainnet-d71aebcb687c/api.token"

./gominer \
  -url stratum+tcp://127.0.0.1:3333 \
  -user qday.rig1
```

macOS:

```sh
./qday-stratum \
  -token-file "$HOME/Library/Application Support/qday/qday-mainnet-d71aebcb687c/api.token"

./gominer \
  -url stratum+tcp://127.0.0.1:3333 \
  -user qday.rig1
```

Windows PowerShell:

```powershell
.\qday-stratum.exe `
  -token-file "$env:APPDATA\qday\qday-mainnet-d71aebcb687c\api.token"

.\gominer.exe `
  -url stratum+tcp://127.0.0.1:3333 `
  -user qday.rig1
```

The data-directory suffix is the first 12 characters of QDAY mainnet's genesis
ID. If QDAY uses a custom `--data` directory, point `-token-file` at the
`api.token` inside that directory.

The worker name is only a label in bridge logs. It is not a payout address.
Changing it cannot redirect a reward. A successful block produces a line like:

```text
level=INFO msg="BLOCK ACCEPTED" height=12345 worker=qday.rig1 block=0000...
```

The 8 QDAY block reward, every included transaction fee and the usual 60-block
maturity rule are enforced by the QDAY node.

Read the [complete setup guide](docs/setup.md) before pointing hardware at it.

## Miner compatibility

The wire protocol follows SiaMining Stratum: `mining.subscribe`,
`mining.authorize`, `mining.set_difficulty`, `mining.notify` and
`mining.submit`. It deliberately returns an empty `extranonce1` and an
`extranonce2_size` of zero because the rightmost QDAY transaction is already
signed. Altering it would create a beautiful hash for an invalid transaction.

The protocol path matches gominer's Sia implementation. SiaMining-derived GPU
miners and hardware that implement the same dialect can use it; vendor firmware
that invented its own dialect needs its own adapter. See
[protocol.md](docs/protocol.md) for the exact messages and byte order.

Use one miner process to manage all GPUs on a machine. Separate miners receiving
the same solo job can search the same nonce range. The bridge publishes a fresh
timestamped job every second for older GPU miners that search only a 32-bit
nonce loop.

## LAN hardware

The default listener is `127.0.0.1:3333`, so only a miner on the same computer
can reach it. For an ASIC or another computer on a trusted LAN:

```sh
qday-stratum -listen 0.0.0.0:3333 -token-file /path/to/api.token
```

Allow TCP `3333` only from the miner's IP in the host firewall. Keep QDAY's
`19770` API on loopback. Stratum authorization is a worker label, not an access
control system, and every connected device mines to this QDAY wallet.

Do not put port `3333` on the public internet unless accepting random work and
random traffic is part of the plan. Never expose `19770` or copy `api.token` to
the miner.

## Options

```text
  -listen string
        Sia Stratum listen address (default "127.0.0.1:3333")
  -node string
        local QDAY node API URL (default "http://127.0.0.1:19770")
  -token-file string
        path to the QDAY api.token file
  -job-interval duration
        fresh-job interval for 32-bit GPU nonce loops (default 1s)
  -version
        print version and exit
```

`Ctrl+C` stops the bridge. It does not stop QDAY Wallet or the external miner.

## Build

Go 1.26 or newer:

```sh
go test ./...
go build -trimpath -o qday-stratum ./cmd/qday-stratum
```

Release archives are built for Linux x86-64, Linux ARM64, Windows x86-64,
macOS Intel and macOS Apple Silicon.

## What actually happens

The QDAY node chooses the parent, payout, mandatory miner marker and valid
mempool transactions. It returns the complete block plus the left-side Merkle
roots needed by a Sia Stratum miner. The bridge uses the final transaction as
the protocol's fixed arbitrary transaction and sends the target and 80-byte
work layout to the miner.

When a miner submits a nonce, the bridge reconstructs the header, checks the
BLAKE2b-256 result against the exact QDAY target, inserts the nonce and submitted
timestamp into the complete encoded block, and calls QDAY `submitblock`. QDAY
then performs normal consensus validation and P2P relay.

That is the whole trick. The miner does hashes. The node decides what a block
means. Nobody gets to mine empty blocks by accident and call it integration.

## Links

- [QDAY](https://pqday.com)
- [QDAY source](https://github.com/petoshi/qday)
- [QDAY explorer](https://explorer.pqday.com)
- [SiaMining Stratum specification](https://github.com/SiaMining/Stratum/blob/master/Stratum.md)
- [petoshi](https://x.com/_petoshi)

## License

MIT. See [LICENSE](LICENSE).
