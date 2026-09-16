# Setup

This guide connects one Sia-compatible miner to one local QDAY wallet. Rewards
go directly to that wallet.

## 1. Prepare QDAY

Install QDAY Wallet or Node v0.8.0 or newer. Start it and wait until the footer
reports `SYNCED`.

Create or import the wallet, then unlock it. The bridge requests a fresh payout
address from the loaded wallet keys whenever QDAY builds a block template. The
seed phrase and wallet password never leave QDAY.

The built-in CPU miner and the external miner are independent. Leave CPU mining
stopped if only the GPU or ASIC should hash.

QDAY must remain open, unlocked and connected while the external miner runs.
Locking or closing the wallet invalidates current work and disconnects the
miner. The bridge stays open and the miner can reconnect after QDAY returns and
the wallet is unlocked again.

## 2. Find `api.token`

The mainnet directory is named `qday-mainnet-d71aebcb687c`.

| System | Default token path |
| --- | --- |
| Linux | `~/.config/qday/qday-mainnet-d71aebcb687c/api.token` |
| macOS | `~/Library/Application Support/qday/qday-mainnet-d71aebcb687c/api.token` |
| Windows | `%APPDATA%\qday\qday-mainnet-d71aebcb687c\api.token` |

A custom QDAY `--data` directory has its own `api.token`. Use that file.

Treat the token like a local wallet-control credential. Do not paste its
contents into a command, a miner configuration or a support message. Give the
bridge the file path with `-token-file`.

## 3. Start the bridge

Linux:

```sh
./qday-stratum -token-file "$HOME/.config/qday/qday-mainnet-d71aebcb687c/api.token"
```

macOS:

```sh
./qday-stratum -token-file "$HOME/Library/Application Support/qday/qday-mainnet-d71aebcb687c/api.token"
```

Windows PowerShell:

```powershell
.\qday-stratum.exe -token-file "$env:APPDATA\qday\qday-mainnet-d71aebcb687c\api.token"
```

Expected startup:

```text
level=INFO msg="Sia Stratum ready" listen=127.0.0.1:3333
level=INFO msg="new QDAY work" height=... job=... workers=0 difficulty=...
```

The bridge automatically reads `node.json` beside `api.token`, including the
random local API port selected by QDAY Wallet. It follows that file if the
wallet restarts on another port. A standalone node still uses
`http://127.0.0.1:19770`. Do not copy a temporary port into the command.

If the listener starts but templates fail, read the reported QDAY error. The
common causes are a locked wallet, an unsynchronized node or an older QDAY
binary without Stratum template data.

## 4. Connect gominer

Install a gominer build with Sia Stratum support and the OpenCL driver required
by the GPU.

```sh
gominer \
  -url stratum+tcp://127.0.0.1:3333 \
  -user qday.rig1
```

On Windows, use the same flags with `gominer.exe`.

`qday.rig1` is a log label. It can be any short nonempty name. QDAY ignores it
for payment and uses the wallet that created the template.

The miner should report authorization, a new job and a nonzero hashrate. The
bridge reports `workers=1` on subsequent jobs.

## 5. Connect a LAN ASIC

Run the bridge on the QDAY computer with a LAN listener:

```sh
qday-stratum \
  -listen 0.0.0.0:3333 \
  -token-file /path/to/api.token
```

Configure the miner with:

```text
URL:      stratum+tcp://QDAY-COMPUTER-LAN-IP:3333
Worker:   qday.asic1
Password: x
```

Some dashboards want `stratum+tcp://`; others want only `IP:PORT`. The firmware
must implement the SiaMining dialect, not Bitcoin Stratum with BLAKE2b selected
from a menu.

Restrict TCP `3333` to the ASIC address. Do not change QDAY's API from
`127.0.0.1:19770`, and do not install the API token on the ASIC.

## 6. Read the result

A solved network block looks like:

```text
level=INFO msg="BLOCK ACCEPTED" height=... worker=qday.rig1 block=0000...
```

The new block should appear in the wallet and at
[explorer.pqday.com](https://explorer.pqday.com). Its miner reward appears as
immature until 60 more blocks are confirmed. Fees in the selected mempool
transactions are part of the same payout.

Shares below the network target are not stored because this is solo mining.
Hashrate stays in the miner interface; QDAY only sees a result that can become
a block.

## Tuning

The default `-job-interval 1s` changes the timestamp once per second. This gives
older GPU miners a fresh job before they repeat a 32-bit nonce range. A miner
that searches the complete 64-bit header nonce can use a quieter interval:

```sh
qday-stratum -job-interval 10s -token-file /path/to/api.token
```

The bridge also replaces work immediately when the QDAY parent or mempool
changes. The node's long poll is the source of that update; the timer only
refreshes nonce space.

Run one miner process per bridge when possible. A single gominer process can
manage several GPUs without duplicating its own nonce ranges. Independent
clients receiving the same solo job may duplicate work.

## Troubleshooting

### `QDAY template unavailable: unlock the payout wallet`

Unlock QDAY Wallet. The bridge cannot choose a payout without loaded keys.

### `waiting for synchronization with a network peer`

Wait for QDAY to show `SYNCED` and at least one connection. Check the node's
peer settings if it never synchronizes.

### `QDAY node has no Sia Stratum template data; update QDAY`

The node predates the API extension required by this bridge. Install QDAY
v0.8.0 or newer.

### `read QDAY API token: ...`

Check the path, quoting and selected QDAY data directory. On macOS, the space in
`Application Support` requires quotes. On Windows PowerShell, use `$env:APPDATA`.

### Miner cannot connect

Confirm the bridge says `Sia Stratum ready`, then check the miner URL and port.
For LAN mining, allow inbound TCP `3333` in the host firewall. Keep the default
loopback listener for a miner on the same computer.

### Repeated `low difficulty share`

The miner is submitting work that does not meet the QDAY network target. Verify
that it uses the SiaMining Stratum dialect and accepts fractional Stratum
difficulty. A Bitcoin Stratum client is not compatible.

### `stale or unknown job`

The parent changed or the job aged out. An occasional stale submission during a
new block is normal. Repeated stale work means the miner is ignoring
`mining.notify` with `clean_jobs=true`.

### QDAY rejects a solved block

Use the exact error printed after `QDAY node rejected block`. Check system time
first. If the same error repeats on current work, include the bridge version,
miner name and that error when reporting the issue. Never include `api.token`,
the wallet password or the seed phrase.
